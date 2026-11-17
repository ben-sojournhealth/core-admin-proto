// Package scenario is a library of claim scenarios and edge cases. Each
// scenario is a script of sim.Actions with the outcome expected after
// every step, so the same definitions drive the demo UI's walkthroughs
// and the test suite (see TestScenarios).
package scenario

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/adjudication"
	"coreadmin/internal/claims"
	"coreadmin/internal/sim"
	"coreadmin/internal/store"
)

type Scenario struct {
	ID      string
	Group   string
	Title   string
	Summary string
	Steps   []Step
}

type Step struct {
	Label string
	// Why explains what this step demonstrates.
	Why    string
	Action sim.Action
	// Rejected, when set, means the action is expected to be refused with
	// an error containing this text.
	Rejected string
	Checks   []Check
}

// Check is one expected fact about the system after a step.
type Check struct {
	Text string
	fn   func(*sim.Sim, *sim.Result) error
}

type CheckResult struct {
	Text   string `json:"text"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

type StepResult struct {
	Pass   bool          `json:"pass"`
	Error  string        `json:"error,omitempty"`
	Checks []CheckResult `json:"checks"`
}

// Run applies a step and evaluates its expectations.
func (st *Step) Run(s *sim.Sim) (*sim.Result, StepResult) {
	res := s.Apply(st.Action)
	out := StepResult{Pass: true, Checks: []CheckResult{}}
	if st.Rejected != "" {
		cr := CheckResult{Text: "Refused: " + st.Rejected, Pass: !res.OK && strings.Contains(res.Error, st.Rejected)}
		if res.OK {
			cr.Detail = "the action was accepted"
		} else if !cr.Pass {
			cr.Detail = "refused with: " + res.Error
		}
		out.Checks = append(out.Checks, cr)
	} else if !res.OK {
		out.Error = res.Error
		out.Pass = false
	}
	for _, c := range st.Checks {
		cr := CheckResult{Text: c.Text, Pass: true}
		if err := c.fn(s, res); err != nil {
			cr.Pass, cr.Detail = false, err.Error()
		}
		out.Checks = append(out.Checks, cr)
	}
	for _, c := range out.Checks {
		out.Pass = out.Pass && c.Pass
	}
	return res, out
}

// ---- checks ----

var names = map[string]string{"mem_1001": "Jordan", "mem_1002": "Sam", "mem_1003": "Riley", "mem_1004": "Avery"}

func claim(s *sim.Sim, id string) (*claimsv1.Claim, error) {
	c, err := s.Repos.Claims.Get(context.Background(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no claim %s", id)
	}
	return c, err
}

func want[T comparable](got, want T) error {
	if got != want {
		return fmt.Errorf("got %v", got)
	}
	return nil
}

func wantUSD(got, want int64) error {
	if got != want {
		return fmt.Errorf("got %s", claims.USD(got))
	}
	return nil
}

func onClaim(text, id string, f func(*claimsv1.Claim) error) Check {
	return Check{Text: text, fn: func(s *sim.Sim, _ *sim.Result) error {
		c, err := claim(s, id)
		if err != nil {
			return err
		}
		return f(c)
	}}
}

func status(id, st string) Check {
	return onClaim(fmt.Sprintf("%s is %s", id, st), id, func(c *claimsv1.Claim) error {
		return want(strings.TrimPrefix(c.GetStatus().String(), "CLAIM_STATUS_"), st)
	})
}

func pays(id string, cents int64) Check {
	return onClaim(fmt.Sprintf("%s: plan pays %s", id, claims.USD(cents)), id, func(c *claimsv1.Claim) error {
		return wantUSD(c.GetAdjudication().GetTotalPaid().GetCents(), cents)
	})
}

func owes(id string, cents int64) Check {
	return onClaim(fmt.Sprintf("%s: member owes %s", id, claims.USD(cents)), id, func(c *claimsv1.Claim) error {
		return wantUSD(c.GetAdjudication().GetTotalMemberResponsibility().GetCents(), cents)
	})
}

func allowed(id string, cents int64) Check {
	return onClaim(fmt.Sprintf("%s: allowed amount is %s", id, claims.USD(cents)), id, func(c *claimsv1.Claim) error {
		return wantUSD(c.GetAdjudication().GetTotalAllowed().GetCents(), cents)
	})
}

func tier(id, t string) Check {
	return onClaim(fmt.Sprintf("%s is priced in tier %s", id, t), id, func(c *claimsv1.Claim) error {
		return want(c.GetAdjudication().GetNetworkTierId(), t)
	})
}

func run(id string, n int32) Check {
	return onClaim(fmt.Sprintf("%s is on adjudication run %d", id, n), id, func(c *claimsv1.Claim) error {
		return want(c.GetAdjudication().GetRunNumber(), n)
	})
}

func pends(id string, codes ...string) Check {
	return onClaim(fmt.Sprintf("%s pends for %s", id, strings.Join(codes, ", ")), id, func(c *claimsv1.Claim) error {
		var got []string
		for _, p := range c.GetAdjudication().GetPends() {
			got = append(got, p.GetCode())
		}
		if !slices.Equal(got, codes) {
			return fmt.Errorf("got %v", got)
		}
		return nil
	})
}

func denied(id, code string) Check {
	return onClaim(fmt.Sprintf("%s is denied for %s", id, code), id, func(c *claimsv1.Claim) error {
		return want(c.GetLines()[0].GetAdjudication().GetDenial().GetCode(), code)
	})
}

func payment(id, state string) Check {
	return onClaim(fmt.Sprintf("%s payment is %s", id, state), id, func(c *claimsv1.Claim) error {
		if c.GetPayment() == nil {
			return errors.New("no payment")
		}
		return want(strings.TrimPrefix(c.GetPayment().GetState().String(), "PAYMENT_STATE_"), state)
	})
}

func noPayment(id string) Check {
	return onClaim(id+" has no payment", id, func(c *claimsv1.Claim) error {
		if c.GetPayment() != nil {
			return fmt.Errorf("payment is %s", c.GetPayment().GetState())
		}
		return nil
	})
}

func hold(id string, on bool) Check {
	text := id + " is on payment hold"
	if !on {
		text = id + " is not on payment hold"
	}
	return onClaim(text, id, func(c *claimsv1.Claim) error {
		return want(c.GetPaymentHold() != nil, on)
	})
}

func openFlags(id string, n int) Check {
	text := fmt.Sprintf("%s has %d open review flag(s)", id, n)
	if n == 0 {
		text = id + " has no open review flags"
	}
	return onClaim(text, id, func(c *claimsv1.Claim) error {
		got := 0
		for _, f := range c.GetReviewFlags() {
			if f.GetClearedTime() == nil {
				got++
			}
		}
		return want(got, n)
	})
}

func noClaim(id string) Check {
	return Check{Text: id + " was not saved", fn: func(s *sim.Sim, _ *sim.Result) error {
		if _, err := s.Repos.Claims.Get(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
			return errors.New("the claim exists")
		}
		return nil
	}}
}

// accum checks one accumulator balance. Family accumulators are owned by
// the subscriber.
func accum(accumulatorID, owner string, cents int64) Check {
	who := names[owner]
	if strings.HasSuffix(accumulatorID, "_FAM") {
		who = "the family"
	}
	return Check{
		Text: fmt.Sprintf("%s for %s is %s", accumulatorID, who, claims.USD(cents)),
		fn: func(s *sim.Sim, _ *sim.Result) error {
			ctx := context.Background()
			plan, err := s.Repos.Plans.Get(ctx, sim.PlanID)
			if err != nil {
				return err
			}
			id := adjudication.FamilyAccumulatorsID(sim.SubscriberID, sim.PlanID, plan.GetBenefitPeriod().GetStart())
			rec, err := s.Repos.FamilyAccumulators.Get(ctx, id)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return wantUSD(adjudication.Applied(rec, accumulatorID, owner), cents)
		},
	}
}

func delta(basis string, original, projected, d int64) Check {
	return Check{
		Text: fmt.Sprintf("Delta is %s: payee holds %s (%s), replacement pays %s", claims.USD(d), claims.USD(original), basis, claims.USD(projected)),
		fn: func(_ *sim.Sim, r *sim.Result) error {
			if r.Delta == nil {
				return errors.New("no delta returned")
			}
			got := fmt.Sprintf("%s %d %d %d", r.Delta.Basis, r.Delta.Original, r.Delta.Projected, r.Delta.Delta)
			if got != fmt.Sprintf("%s %d %d %d", basis, original, projected, d) {
				return fmt.Errorf("got %s: holds %s, pays %s, delta %s", r.Delta.Basis,
					claims.USD(r.Delta.Original), claims.USD(r.Delta.Projected), claims.USD(r.Delta.Delta))
			}
			return nil
		},
	}
}

func preview(st string, paid, member int64) Check {
	return Check{
		Text: fmt.Sprintf("Projection: %s, plan pays %s, member owes %s", st, claims.USD(paid), claims.USD(member)),
		fn: func(_ *sim.Sim, r *sim.Result) error {
			if r.Preview == nil {
				return errors.New("no projection returned")
			}
			if r.Preview.Status != st || r.Preview.Paid != paid || r.Preview.Member_ != member {
				return fmt.Errorf("got %s, pays %s, owes %s", r.Preview.Status, claims.USD(r.Preview.Paid), claims.USD(r.Preview.Member_))
			}
			return nil
		},
	}
}

func logged(text string) Check {
	return Check{Text: "Log says: “" + text + "”", fn: func(_ *sim.Sim, r *sim.Result) error {
		for _, l := range r.Lines {
			if strings.Contains(l, text) {
				return nil
			}
		}
		return errors.New("not in the log for this step")
	}}
}

// ---- runner ----

// Runner plays scenarios against a sandbox, one step at a time.
type Runner struct {
	Sim     *sim.Sim
	Active  *Scenario
	Next    int
	Results []StepResult
}

func NewRunner() (*Runner, error) {
	r := &Runner{}
	return r, r.Reset()
}

// Reset starts a fresh sandbox with no active scenario.
func (r *Runner) Reset() error {
	s, err := sim.New()
	if err != nil {
		return err
	}
	r.Sim, r.Active, r.Next, r.Results = s, nil, 0, nil
	return nil
}

// Load resets the sandbox and makes a scenario active.
func (r *Runner) Load(id string) error {
	for i := range Library {
		if Library[i].ID == id {
			if err := r.Reset(); err != nil {
				return err
			}
			r.Active = &Library[i]
			return nil
		}
	}
	return fmt.Errorf("no scenario %q", id)
}

// Step runs the active scenario's next step. It reports false when there
// is nothing left to run.
func (r *Runner) Step() (*sim.Result, bool) {
	if r.Active == nil || r.Next >= len(r.Active.Steps) {
		return nil, false
	}
	res, sr := r.Active.Steps[r.Next].Run(r.Sim)
	r.Results = append(r.Results, sr)
	r.Next++
	return res, true
}
