-- Sketch of the eligibility tables in Postgres. Not used yet: the
-- prototype runs on the in-memory store. Every table keeps the record as
-- protojson in `data`, with real columns only for what queries need.

-- 834 member loops exactly as intake captured them.
CREATE TABLE enrollment_transactions (
  id                text PRIMARY KEY,
  ingestion_job_id  text NOT NULL,
  sender_id         text NOT NULL,
  data              jsonb NOT NULL,          -- eligibility.v1.EnrollmentTransaction
  created_at        timestamptz NOT NULL DEFAULT now()
);

-- Everything that can change eligibility, in order. Append-only: no
-- UPDATE or DELETE is ever issued against it.
CREATE TABLE eligibility_inputs (
  sequence       bigint PRIMARY KEY,         -- order of application
  id             text UNIQUE NOT NULL,       -- in_00000001
  kind           text NOT NULL,              -- enrollment | full_file_approval | ...
  enrollment_transaction_id text REFERENCES enrollment_transactions(id),
  recorded_at    timestamptz NOT NULL,
  recorded_by    text NOT NULL,
  data           jsonb NOT NULL              -- eligibility.v1.EligibilityInput
);

-- Current members, as derived from the inputs: what claims read.
CREATE TABLE members (
  id             text PRIMARY KEY,
  customer_id    text NOT NULL,
  subscriber_id  text NOT NULL,
  card_id        text NOT NULL,
  revision       bigint NOT NULL,
  data           jsonb NOT NULL,             -- eligibility.v1.Member, coverage included
  UNIQUE (customer_id, card_id)
);

-- Every version of every member: the audit trail. A version is current
-- from recorded_at until the next version's recorded_at. "What did we
-- believe at T" is the latest version with recorded_at <= T. Append-only.
CREATE TABLE member_versions (
  member_id      text NOT NULL,
  version        int NOT NULL,
  recorded_at    timestamptz NOT NULL,
  input_sequence bigint REFERENCES eligibility_inputs(sequence),  -- NULL for a regeneration
  rules_version  text NOT NULL,
  reason         text,
  data           jsonb NOT NULL,             -- eligibility.v1.Member as it was
  PRIMARY KEY (member_id, version)
);
CREATE INDEX member_versions_as_of ON member_versions (member_id, recorded_at DESC);

-- Coverage by effective date, for queries across members ("who was
-- covered on Feb 15 as we knew it on Mar 1"). Derived from member_versions,
-- one row per span per version: valid time (the coverage dates) crossed
-- with recorded time (the version's lifetime).
CREATE TABLE coverage_history (
  member_id      text NOT NULL,
  version        int NOT NULL,
  plan_id        text NOT NULL,
  line           text NOT NULL,
  effective      daterange NOT NULL,         -- valid time
  recorded       tstzrange NOT NULL,         -- [recorded_at, next version's recorded_at)
  FOREIGN KEY (member_id, version) REFERENCES member_versions
);
CREATE INDEX coverage_history_bitemporal ON coverage_history USING gist (effective, recorded);
