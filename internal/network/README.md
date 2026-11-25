# Network

Provider data, network vendor intake and the provider portal. Out of scope for '28: contracting, credentialing, roster intake (shown on the domain map, contracted out).

A **network** is what a customer's plan opts into (`PlanVersion.network_id`). It is an ordered list of **underlying networks** (direct contracts, a wrap, a leased network), each placed in one of the network's tiers. Providers participate in underlying networks. When a provider is in several, the first one in the network's list wins: its tier applies and its contract prices the claim. The placement is the same for every customer on the network; the plan only says what each tier costs the member.

A **provider** is who an NPI is. A **participation** says that NPI, billing under a TIN, is in an underlying network for one or more periods; it has its own table, and it is the roll-up in both directions: an NPI's participations are the TINs it bills under, a TIN's are its providers. An underlying network says who prices its claims: we hold the contracts (a participation names one), or the vendor reprices (no contract on the participation).

Other domains ask it through `NetworkService` (`proto/coreadmin/network/v1/network_service.proto`): a provider by NPI with its participations under a TIN on a date and, given a network, its tier in that network and the participation that put it there; and a contract's terms. Answers use the latest data we have, about the date asked. Network status on a claim is NPI + TIN + date; locations are for the directory. Pricing from contracts belongs to Claims.

| Package | |
|---|---|
| `internal/network` | NetworkService |
| `networkdb` | The Network schema's tables and stores: providers, participations, networks, underlying networks, contracts, vendor files and their updates |
| `vendorintake` | A network vendor's full provider files: one proposed update per file, applied on approval |

## Vendor Intake

`/provider-intake/`: Cigna's individual-provider files, one **source** per file feed (PPO and OAP, each feeding its own underlying network). Send the sample files in order, review each update, approve or reject it, and look up an NPI's TINs or a TIN's NPIs.

- **A file is the source's whole statement, history included.** One row per NPI and TIN, with more rows when a provider has more than four specialties or earlier periods of participation. A file speaks only for its own source.
- **Rows are kept as sent** (`VendorProviderRow`): the columns we act on in fields, every other column by its header. Nothing is looked up or cleaned. A row with no NPI, no TIN or an unreadable date is left out and counted.
- **Each file proposes one update** (`ProviderUpdate`): its rows are compared with what the source's last approved file said, per NPI and TIN, and only the differences are stored (`VendorProviderChange`: added, changed, dropped). The review shows the breadth: counts, what changed, where. Nothing changes provider data until the update is approved as a whole; rejecting it applies nothing. A source takes no new file while one awaits a decision.
- **Approval derives the participations.** A pair's participation is a pure function of its rows in the latest approved file (`vendorintake.Derive`): the periods are the rows' start and term dates, and nothing is remembered from older files. A provider record is created for an NPI we don't have.
- **A pair missing from a file is removed for every date**, since the file is a full history: a claim for a past date of service reads out-of-network afterwards. The review flags these separately from terms (a term date arriving on a row). Whether a vanished row really means this is to be confirmed with Cigna before launch.
- **Scale.** Files are 20 GB and growing. Rows stream into a per-file working table and are compared with the source's participations in Postgres (`networkdb.FileRows`); writes go in batches. The working table is the one table that isn't versioned: it is dropped once the update is built.

The column headers are placeholders (`vendorintake/sources.go`) until we have Cigna's file specification.

Not built yet: the facility files (keyed by TIN and address rather than NPI; waiting on the layout), storing the raw file (with the SFTP integrations), how a repriced claim is priced (Claims), roster intake, and the provider portal (claim status, disputes; prior auths are out of scope for '28).
