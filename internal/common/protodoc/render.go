package protodoc

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Contract is a service's interface as one domain uses it: the service's
// methods (marked when used), and the source of every message and enum
// the used methods send or return, grouped by protobuf package.
type Contract struct {
	Service string `json:"service"`
	File    string `json:"file"`
	Doc     string `json:"doc,omitempty"`
	// The service as written, with every method.
	Source   string    `json:"source"`
	Methods  []Method  `json:"methods"`
	Packages []Package `json:"packages"`
}

type Method struct {
	Name     string `json:"name"`
	Doc      string `json:"doc,omitempty"`
	Request  string `json:"request"`
	Response string `json:"response"`
	Used     bool   `json:"used"`
}

// Package is the types of one protobuf package the contract uses.
type Package struct {
	Name  string `json:"name"`
	Types []Type `json:"types"`
}

// Type is one message or enum, rendered as .proto source with its
// comments.
type Type struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Source string `json:"source"`
}

// Interface renders a service (by full name) for the methods a caller
// uses, and every type those methods carry.
func Interface(service string, used []string) (*Contract, error) {
	sd, ok := load().services[service]
	if !ok {
		return nil, fmt.Errorf("protodoc: no service %s", service)
	}
	pkg := string(sd.ParentFile().Package())
	c := &Contract{Service: service, File: sd.ParentFile().Path(), Doc: comment(sd)}
	isUsed := map[string]bool{}
	for _, u := range used {
		isUsed[u] = true
	}
	var roots []protoreflect.Descriptor
	var src strings.Builder
	writeComment(&src, "", sd)
	fmt.Fprintf(&src, "service %s {\n", sd.Name())
	for i := 0; i < sd.Methods().Len(); i++ {
		m := sd.Methods().Get(i)
		name := string(m.Name())
		if len(used) > 0 && i > 0 {
			src.WriteString("\n")
		}
		writeComment(&src, "  ", m)
		fmt.Fprintf(&src, "  rpc %s(%s) returns (%s);\n", name, rel(m.Input().FullName(), pkg), rel(m.Output().FullName(), pkg))
		c.Methods = append(c.Methods, Method{Name: name, Doc: comment(m),
			Request: string(m.Input().FullName()), Response: string(m.Output().FullName()), Used: isUsed[name]})
		if isUsed[name] {
			roots = append(roots, m.Input(), m.Output())
		}
		delete(isUsed, name)
	}
	src.WriteString("}\n")
	c.Source = src.String()
	for name := range isUsed {
		return nil, fmt.Errorf("protodoc: %s has no method %s", service, name)
	}

	// Every type the used methods carry, in order of first use.
	seen := map[string]bool{}
	byPkg := map[string]*Package{}
	var order []string
	var visit func(d protoreflect.Descriptor)
	visit = func(d protoreflect.Descriptor) {
		name := string(d.FullName())
		if seen[name] || strings.HasPrefix(name, "google.protobuf.") {
			return
		}
		seen[name] = true
		p := string(d.ParentFile().Package())
		if byPkg[p] == nil {
			byPkg[p] = &Package{Name: p}
			order = append(order, p)
		}
		byPkg[p].Types = append(byPkg[p].Types, Type{Name: name, File: d.ParentFile().Path(), Source: render(d, p)})
		if m, ok := d.(protoreflect.MessageDescriptor); ok {
			for i := 0; i < m.Fields().Len(); i++ {
				f := m.Fields().Get(i)
				if f.IsMap() {
					f = f.MapValue()
				}
				if f.Message() != nil {
					visit(f.Message())
				} else if f.Enum() != nil {
					visit(f.Enum())
				}
			}
		}
	}
	for _, r := range roots {
		visit(r)
	}
	// The service's own package first, then in order of first use.
	sort.SliceStable(order, func(i, j int) bool { return order[i] == pkg && order[j] != pkg })
	for _, p := range order {
		c.Packages = append(c.Packages, *byPkg[p])
	}
	return c, nil
}

// rel is a type's name as written in package pkg: short in its own
// package, without the shared "coreadmin." prefix otherwise.
func rel(name protoreflect.FullName, pkg string) string {
	n := string(name)
	if p := pkg + "."; strings.HasPrefix(n, p) {
		return strings.TrimPrefix(n, p)
	}
	return strings.TrimPrefix(n, "coreadmin.")
}

func writeComment(b *strings.Builder, indent string, d protoreflect.Descriptor) {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	c := strings.TrimRight(loc.LeadingComments, "\n ")
	if strings.TrimSpace(c) == "" {
		return
	}
	for _, line := range strings.Split(c, "\n") {
		fmt.Fprintf(b, "%s//%s\n", indent, strings.TrimRight(line, " "))
	}
}

// render is a message or enum as .proto source, with its comments.
func render(d protoreflect.Descriptor, pkg string) string {
	var b strings.Builder
	writeComment(&b, "", d)
	name := strings.TrimPrefix(string(d.FullName()), pkg+".")
	switch d := d.(type) {
	case protoreflect.EnumDescriptor:
		fmt.Fprintf(&b, "enum %s {\n", name)
		for i := 0; i < d.Values().Len(); i++ {
			v := d.Values().Get(i)
			writeComment(&b, "  ", v)
			fmt.Fprintf(&b, "  %s = %d;\n", v.Name(), v.Number())
		}
	case protoreflect.MessageDescriptor:
		fmt.Fprintf(&b, "message %s {\n", name)
		var oneof protoreflect.OneofDescriptor
		for i := 0; i < d.Fields().Len(); i++ {
			f := d.Fields().Get(i)
			o := f.ContainingOneof()
			if o != nil && o.IsSynthetic() {
				o = nil
			}
			indent := "  "
			if o != oneof {
				if oneof != nil {
					b.WriteString("  }\n")
				}
				if o != nil {
					fmt.Fprintf(&b, "  oneof %s {\n", o.Name())
				}
				oneof = o
			}
			if o != nil {
				indent = "    "
			}
			writeComment(&b, indent, f)
			fmt.Fprintf(&b, "%s%s %s = %d;\n", indent, fieldType(f, pkg), f.Name(), f.Number())
		}
		if oneof != nil {
			b.WriteString("  }\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

func fieldType(f protoreflect.FieldDescriptor, pkg string) string {
	typ := func(f protoreflect.FieldDescriptor) string {
		switch {
		case f.Message() != nil:
			return rel(f.Message().FullName(), pkg)
		case f.Enum() != nil:
			return rel(f.Enum().FullName(), pkg)
		}
		return f.Kind().String()
	}
	switch {
	case f.IsMap():
		return fmt.Sprintf("map<%s, %s>", typ(f.MapKey()), typ(f.MapValue()))
	case f.IsList():
		return "repeated " + typ(f)
	case f.HasOptionalKeyword():
		return "optional " + typ(f)
	}
	return typ(f)
}
