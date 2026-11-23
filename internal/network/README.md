# Network

Provider data, network vendor intake and the provider portal. Out of scope for '28: contracting, credentialing, roster intake (shown on the domain map, contracted out).

A **network** is what a customer's plan opts into (`PlanVersion.network_id`). It is an ordered list of **underlying networks** (direct contracts, a wrap, a leased network), each placed in one of the network's tiers. Providers participate in underlying networks. When a provider is in several, the first one in the network's list wins: its tier applies and its contract prices the claim. The placement is the same for every customer on the network; the plan only says what each tier costs the member.

Other domains ask it through `NetworkService` (`proto/coreadmin/network/v1/network_service.proto`): a provider by NPI with its participations under a TIN on a date and, given a network, its tier in that network and the participation that put it there; and a contract's terms. Answers use the latest data we have, about the date asked. Pricing from contracts belongs to Claims.

| Package | |
|---|---|
| `internal/network` | NetworkService |
| `networkdb` | The Network schema's tables and stores: providers, networks, underlying networks, contracts |

Not built yet: vendor intake, the provider portal (claim status, disputes; prior auths are out of scope for '28).
