# Investigation Playbook: ClickHouse + ZooKeeper

A methodology for diagnosing replication, ZooKeeper/Keeper, and counter-anomaly issues using the read-only system tables exposed via this MCP server.

Written from lessons learned during real production investigations. The goal is to avoid common dead ends — particularly the trap of "the source code says X but the data shows Y, so it must be a bug."

## Core principle

> When source code seems to contradict observed data, the workload is almost certainly using a code path you haven't traced yet. Map the workload before forming a hypothesis.

The single biggest time-saver is enumerating *what operations actually touch the relevant tables* before forming a theory about *why* a counter or metric is moving.

## Standard first-pass triage

When investigating any counter spike, error pattern, or replication anomaly on a Replicated table, run these queries in order. Each one narrows the hypothesis space.

### 1. What workload touches the table?

```sql
-- All DDL + structural ALTERs on the table in the last hour
SELECT
    event_time, hostName() AS host, query_kind,
    substring(query, 1, 200) AS query
FROM clusterAllReplicas('<cluster>', system.query_log)
WHERE event_time > now() - INTERVAL 1 HOUR
  AND type = 'QueryFinish'
  AND query ILIKE '%<table_name>%'
  AND query_kind IN ('Create', 'Drop', 'Alter', 'Truncate', 'Rename', 'System')
ORDER BY event_time DESC
LIMIT 50;
```

`CREATE OR REPLACE TABLE`, `DROP TABLE`, `TRUNCATE`, `ALTER … REPLACE PARTITION`, `ATTACH PARTITION`, and `RESTART REPLICA` all have very different effects on replication state — and they all show up here.

### 2. Is the issue ensemble-wide or single-node?

```sql
-- Count error events per host in last hour
SELECT
    hostName() AS host, name, value, last_error_time,
    substring(last_error_message, 1, 200) AS msg
FROM clusterAllReplicas('<cluster>', system.errors)
WHERE value > 0
  AND last_error_time > now() - INTERVAL 1 HOUR
ORDER BY last_error_time DESC
LIMIT 50;
```

Single-host bursts usually mean local saturation (query queue, disk, network). Cluster-wide bursts mean ZK ensemble or shared infrastructure.

### 3. Is the table actually readonly?

```sql
-- Both gauge (right-now) and historical (last hour)
SELECT
    hostName() AS host,
    event_time,
    CurrentMetric_ReadonlyReplica AS readonly_count
FROM clusterAllReplicas('<cluster>', system.metric_log)
WHERE event_time > now() - INTERVAL 1 HOUR
  AND CurrentMetric_ReadonlyReplica > 0
ORDER BY event_time DESC
LIMIT 50;
```

`CurrentMetric_ReadonlyReplica > 0` is the most reliable indicator of a real replication problem. If this is zero throughout an incident, the table was never actually impaired — even if other counters were ticking.

### 4. Did any ZooKeeper session actually expire?

```sql
-- Authoritative log for real session-expiry events
SELECT
    hostName() AS host, event_time,
    extract(logger_name, '\((.+?)\)') AS thread,
    logger_name,
    substring(message, 1, 200) AS msg
FROM clusterAllReplicas('<cluster>', system.text_log)
WHERE event_time > now() - INTERVAL 1 HOUR
  AND message ILIKE '%ZooKeeper session has expired%'
ORDER BY event_time DESC
LIMIT 50;
```

The `LOG_WARNING "ZooKeeper session has expired. Switching to a new session."` line from `ReplicatedMergeTreeRestartingThread` is emitted *exactly once per session-expiry event per table* on the affected host. If a node's ZK session truly expired, you will see one line per Replicated table on that host (often dozens to hundreds of identical lines clustered in a 1-second window).

## Counter pitfalls

Some `ProfileEvent_*` counters are easy to misread. Two common ones:

### `ReplicaPartialShutdown`

**Documentation says**: "How many times Replicated table has to deinitialize its state due to session expiration in ZooKeeper."

**Reality**: incremented in `StorageReplicatedMergeTree::partialShutdown()`, which is called both for genuine ZK session expiry **and** for the table's full-shutdown path. So this counter ticks on:

- Real ZK session expiration (with `LOG_WARNING`, readonly tick, ~N ticks per host where N = number of Replicated tables on that host)
- `DROP TABLE` / `DETACH TABLE` / `RENAME` / **`CREATE OR REPLACE TABLE`**
- Server shutdown / table flush-prepare

The full-shutdown path does **not** produce a `LOG_WARNING`, does **not** tick `CurrentMetric_ReadonlyReplica`, and only affects the one table being shut down.

So if you see this counter incrementing with no session-expiry log line and no readonly metric, look for `CREATE OR REPLACE TABLE` or similar DDL on the affected tables — it's likely a workload pattern, not a problem.

### `metric_log.ProfileEvent_*` semantics

The columns in `system.metric_log` are **per-second deltas** of the global counter, not the cumulative value. Verify with a known-rate counter:

```sql
-- Should roughly match: total Query counter delta vs query_log count
SELECT sum(ProfileEvent_Query) AS metric_log_delta
FROM system.metric_log
WHERE event_time > now() - INTERVAL 10 MINUTE;

SELECT count() AS query_log_total
FROM system.query_log
WHERE event_time > now() - INTERVAL 10 MINUTE AND type = 'QueryStart';
```

If they don't match, your understanding of the column is wrong.

## Decision tree: counter incrementing with no obvious cause

```
Counter is incrementing every X minutes
│
├─ All hosts simultaneously?
│  ├─ Yes → workload-driven (DDL ON CLUSTER, scheduled job, replication broadcast)
│  │       → Query system.query_log for that table; check for Create/Drop/Alter at the burst times
│  │
│  └─ No (one host or subset) → host-local cause
│           → Check system.errors per host (saturation, disk, network)
│           → Check system.text_log for that host (session expiry, hardware errors)
│
├─ Does the side-effect signature match?
│  ├─ readonly tick + log line + N-table cascade → real session expiry
│  ├─ readonly tick + no log line + just this one table → table-level startup failure
│  └─ no readonly tick + no log line + just this one table → likely full-shutdown path (DDL)
│
└─ When source code "rules out" the observed pattern
   → You're looking at the wrong path. Re-grep with the full set of callers.
   → Or the workload is exercising a path not described in the counter's docs.
```

## When CH system tables aren't enough

Some questions cannot be answered from `system.*`:

- **What ZK transactions actually executed?** Use `zkTxnLogToolkit.sh` on the ZK ensemble against `/var/lib/zookeeper/version-2/log.*` (path varies). Pipe through `grep -a` since data values are binary.
- **What did the ZK server log say?** `/var/log/zookeeper/*.out` or `*.log` on each ensemble node, depending on logback config. Note default INFO level filters out routine client requests.
- **Is the ephemeral `is_active` znode for a replica being deleted and recreated?** Query `system.zookeeper` with the path from `system.replicas.replica_path` + `/is_active`. Check `ctime` and `ephemeralOwner` (session ID): if both are stable over many counter ticks, partialShutdown is not running on *that* table.

### Critical: which table to inspect

When a workload uses staging tables that get dropped/recreated (a common pattern for `REPLACE PARTITION` jobs), the counter ticks come from the *staging* table being shut down. Inspecting the *persistent* target table's `is_active` will mislead you — it never changes. Always cross-reference `system.query_log` to find which table(s) the workload actually creates/drops.

## Productive query patterns

### Find which tables a ZK session ID belongs to

```sql
SELECT zookeeper_path, replica_path, replica_name
FROM clusterAllReplicas('<cluster>', system.replicas)
WHERE database = '<db>';
```

Then walk `system.zookeeper` from `zookeeper_path` to find children and ephemeralOwners.

### Correlate counter bursts with DDL

```sql
-- Per-host counter increments (last 1h)
SELECT event_time, hostName() AS host, ProfileEvent_<NAME> AS tick
FROM clusterAllReplicas('<cluster>', system.metric_log)
WHERE event_time > now() - INTERVAL 1 HOUR
  AND ProfileEvent_<NAME> > 0
ORDER BY event_time;

-- DDL on potentially-related tables, same window
SELECT event_time, hostName() AS host, query_kind, substring(query, 1, 150) AS q
FROM clusterAllReplicas('<cluster>', system.query_log)
WHERE event_time > now() - INTERVAL 1 HOUR
  AND type = 'QueryFinish'
  AND query_kind IN ('Create', 'Drop', 'Alter')
ORDER BY event_time;
```

A 100% temporal correlation between bursts and a specific DDL pattern is usually the answer.

## Alerting recommendations

Based on signal quality observed in real investigations:

- **High signal**: `text_log` matches for `"ZooKeeper session has expired"` — fires on genuine session loss, near-zero false positives.
- **High signal**: `CurrentMetric_ReadonlyReplica > 0` — direct gauge for table impairment.
- **Lower signal**: `ProfileEvent_ReplicaPartialShutdown` — increments on DDL too; use only with a per-host rate threshold high enough to filter out normal DDL noise (e.g. `>5 per host per 5min`).

## Reasoning hygiene

Habits that prevent misadventures:

1. **Map the workload before forming hypotheses.** `system.query_log` GROUP BY query_kind on suspect tables is almost always query #1.
2. **Verify column semantics with a known-rate counter** before reasoning about an unfamiliar metric.
3. **Treat "source says impossible" as a signal to widen the source grep**, not to escalate to "must be a bug."
4. **Check the right table.** If a workload uses ephemeral / staging / `_tmp_*` tables, the persistent target tables can look untouched while the action is happening on tables you didn't initially list.
5. **State explicit predictions before each query.** "If this is X, I expect Y rows. If it's Z, I expect 0." Forces you to notice when the data falsifies your model, instead of rationalizing it.
