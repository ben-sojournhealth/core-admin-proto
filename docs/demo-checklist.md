# Demo checklist: screenshots for broker materials

A potential client asked for a screenshot of the claims platform prototype to put in materials shared with brokers, as substance behind our technical claims. We are sending three. This lists what to build and clean up before capturing them.

## The three screenshots

- [ ] **Claim examiner page.** Today's Journey claim page is too bare to sit beside a mature claims system. Needs designing before building.
- [ ] **Employer portal.** Not built yet. Its own worktree.
- [ ] **System design.** The domain map shows too much of the project's intermediate state (planned demos, out of scope for '28). Replace it for this purpose with a "path of a claim" diagram showing only what is built: 837 in, the adjudication steps, 835 and EOB out.

## Other candidates, already built

Any of these can replace one of the three or make a fourth.

- [ ] **EOB beside its 835** (Remittance).
- [ ] **Member page:** coverage through the year, family, accumulators, claims (Journey, Members).
- [ ] **Eligibility timeline with a retroactive termination,** and the claims it affects (Eligibility).
- [ ] **An adjustment, before and after:** original, replacement, accumulator difference (Claims Lifecycle).
- [ ] **Member cost estimate** from the real engine (ClaimsService estimate).
- [ ] **837 to parsed claim to 999** (Claims Intake). More for a technical buyer than a broker.

## Clean up before any capture

- [x] Hide the worktree badge (the green "project :port" chip, bottom left of every page): add `?capture` to the URL.
- [ ] Hide the "not built" labels in the Journey side menus (Disputes, Billing & funding). Done for the Claims tab with `?capture`, which also hides Architecture and Reset; the other tabs are not done.
- [ ] Hide the "not in order" and "listed, not ordered" captions on the adjudication map.
- [ ] Hide "demo planned" and "out of scope for '28" anywhere they appear in frame.
- [ ] Richer sample data in the Journey sandbox: multi-line claims, more than one provider, full member names, a claim with deductible and coinsurance (today every claim is one line from "Dr. Chen").
- [ ] Frame each page so content fills it; the claim page and queue currently use less than half.
- [ ] Check nothing in frame looks like real data: names, EINs, member ids, NPIs, TINs.

## Capturing

Start a demo (`make demo`, or `go run ./cmd/demo -addr localhost:<port>` from a worktree), then:

```
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu \
  --hide-scrollbars --force-device-scale-factor=2 --window-size=1440,900 \
  --virtual-time-budget=5000 --screenshot=<out>.png "<url>"
```

Useful URLs:

| Page | URL |
|---|---|
| Examiner queue | `/journey/#/q/pended` |
| Claim page | `/journey/?capture#/claim/<claim number>` (the showcase claim is `26072-E-00003`; use `--window-size=1440,1440`) |
| Employer page | `/journey/#/employers` |
| Adjudication map | `/adjudication/` |
| Domain map | `/` |

## Before sending

- [ ] Each image is readable at the size it will appear in the materials.
- [ ] The set is described as a prototype, as the client's request did.
- [ ] Someone who has not seen the system can say what each image shows in one sentence.
