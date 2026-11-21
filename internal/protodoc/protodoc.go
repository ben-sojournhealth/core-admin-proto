// Package protodoc serves the comments written in the .proto files, so a
// UI can explain a message's fields: the Go code generated from the
// protos drops them.
package protodoc

import (
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"coreadmin/gen"
)

// Field explains one field.
type Field struct {
	Doc string `json:"doc,omitempty"`
	// Full name of the field's message or enum type, if it has one.
	Message string `json:"message,omitempty"`
	Enum    string `json:"enum,omitempty"`
}

// Docs is the explanations for a message and every message and enum it
// uses, by full name.
type Docs struct {
	Root     string                       `json:"root"`
	Messages map[string]map[string]Field  `json:"messages"`
	Enums    map[string]map[string]string `json:"enums"`
}

var (
	once  sync.Once
	files *protoFiles
)

type protoFiles struct {
	byName map[string]protoreflect.Descriptor
}

func load() *protoFiles {
	once.Do(func() {
		files = &protoFiles{byName: map[string]protoreflect.Descriptor{}}
		var set descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(gen.Descriptors, &set); err != nil {
			return
		}
		reg, err := protodesc.NewFiles(&set)
		if err != nil {
			return
		}
		reg.RangeFiles(func(f protoreflect.FileDescriptor) bool {
			var walk func(ms protoreflect.MessageDescriptors)
			walk = func(ms protoreflect.MessageDescriptors) {
				for i := 0; i < ms.Len(); i++ {
					m := ms.Get(i)
					files.byName[string(m.FullName())] = m
					walk(m.Messages())
					for j := 0; j < m.Enums().Len(); j++ {
						files.byName[string(m.Enums().Get(j).FullName())] = m.Enums().Get(j)
					}
				}
			}
			walk(f.Messages())
			for i := 0; i < f.Enums().Len(); i++ {
				files.byName[string(f.Enums().Get(i).FullName())] = f.Enums().Get(i)
			}
			return true
		})
	})
	return files
}

// For returns the explanations for a message, by full name, and every
// message and enum reachable from it. Keys are the proto field names, as
// in protojson with UseProtoNames.
func For(fullName string) *Docs {
	d := &Docs{Root: fullName, Messages: map[string]map[string]Field{}, Enums: map[string]map[string]string{}}
	var visit func(name string)
	visit = func(name string) {
		if _, ok := d.Messages[name]; ok {
			return
		}
		if _, ok := d.Enums[name]; ok {
			return
		}
		switch desc := load().byName[name].(type) {
		case protoreflect.MessageDescriptor:
			fields := map[string]Field{}
			d.Messages[name] = fields
			for i := 0; i < desc.Fields().Len(); i++ {
				f := desc.Fields().Get(i)
				fd := Field{Doc: comment(f)}
				switch {
				case f.Message() != nil:
					fd.Message = string(f.Message().FullName())
				case f.Enum() != nil:
					fd.Enum = string(f.Enum().FullName())
				}
				fields[string(f.Name())] = fd
				if fd.Message != "" {
					visit(fd.Message)
				}
				if fd.Enum != "" {
					visit(fd.Enum)
				}
			}
		case protoreflect.EnumDescriptor:
			values := map[string]string{}
			d.Enums[name] = values
			for i := 0; i < desc.Values().Len(); i++ {
				v := desc.Values().Get(i)
				if c := comment(v); c != "" {
					values[string(v.Name())] = c
				}
			}
		}
	}
	visit(fullName)
	return d
}

// comment is a descriptor's leading comment, or failing that its trailing
// one, as one paragraph.
func comment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	c := loc.LeadingComments
	if strings.TrimSpace(c) == "" {
		c = loc.TrailingComments
	}
	return strings.Join(strings.Fields(c), " ")
}
