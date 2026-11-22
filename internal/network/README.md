# Network

Provider data, network vendor intake and the provider portal. Out of scope for '28: contracting, credentialing, roster intake (shown on the domain map, contracted out).

Other domains ask it through `NetworkService` (`proto/coreadmin/network/v1/network_service.proto`): a provider by NPI with its network participations under a TIN on a date, and a contract's terms. Answers use the latest data we have, about the date asked. Pricing from contracts belongs to Claims.

| Package | |
|---|---|
| `internal/network` | NetworkService |
| `networkdb` | The Network schema's tables and stores: providers, networks, contracts |

Not built yet: vendor intake, the provider portal (claim status, disputes; prior auths are out of scope for '28).
