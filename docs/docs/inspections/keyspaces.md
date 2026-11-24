---
sidebar_position: 15.5
---

# Amazon Keyspaces

This inspection finds the reasons why Amazon Keyspaces rejects requests, and the tables that are close to their
provisioned capacity. It uses the CloudWatch metrics that the cluster-agent collects when
[Keyspaces monitoring](/configuration/aws#amazon-keyspaces) is enabled.

Keyspaces returns the same timeout errors to the client for unrelated causes, such as provisioned throttling, a hot
partition or the per-connection limit. The inspection tells these causes apart, so that one fault fires one check.

## Checks

| Check | Fires when | Default threshold |
|-------|------------|-------------------|
| Keyspaces hot partitions | requests to a table are rejected by the throughput limit of a partition | 0 per 5 minutes |
| Keyspaces throttling | read and write throttle events of a table, without the hot partition rejections | 0 per 5 minutes |
| Keyspaces provisioned capacity | the consumed read or write capacity of a provisioned table, as a share of its provisioned capacity | 80% |
| Keyspaces per-connection limit | requests are rejected by the per-connection request rate limit | 0 per 5 minutes |
| Keyspaces system errors | Keyspaces returns system errors for a table | 0 per 5 minutes |
| Keyspaces client errors | invalid requests to a table, without throttling; invalid `EXECUTE` statements, without per-connection limit rejections; invalid `PREPARE` statements | 0 per 5 minutes |

Each check uses the highest value of the last 10 minutes, because CloudWatch publishes data 1.5–3 minutes late. A
shorter window would let an active fault return to OK between two data points.

The provisioned capacity check is OK when all tables are in on-demand capacity mode. All checks are `UNKNOWN` when
there is no CloudWatch data.

Thresholds can be changed per project and per application, like those of the other inspections. The hot partitions,
throttling, per-connection limit and system errors checks have
[built-in alerting rules](/alerting/alerts#built-in-rules).

### Overlapping counters

AWS counts one rejection in several CloudWatch metrics:

* a hot partition rejection also increments the write throttle events of the table;
* a throttled request also increments the `UserErrors` of the table;
* a per-connection limit rejection also increments the `UserErrors` of the `EXECUTE` operation.

The inspection subtracts the overlapping counter, so each rejection goes to one check only. AWS increments the
overlapping counters almost, but not exactly, one-to-one. When the remainder is within 1% of the subtracted counter,
the inspection treats it as the same rejections, not as new ones.

## Tables

The report has one row for each table:

* **Capacity mode**: on-demand, provisioned, or provisioned with auto scaling.
* **Consumed capacity**: for a provisioned table, the read and write capacity in use, as a share of the provisioned capacity.
* **Size**.
* **Issues**: the checks that fired for the table, each with a remedy. The throttling remedy depends on the table:
  * provisioned, without auto scaling: raise the provisioned capacity or enable auto scaling;
  * provisioned, with auto scaling: auto scaling doesn't keep up, raise its minimum or maximum capacity;
  * on-demand: the traffic exceeded the warm throughput or the previous peak of the table, pre-warm the table.

The charts show, for each table, the consumed and provisioned capacity, the p99 latency and the successful requests by
operation, and the rejected requests by cause. One more chart shows the rejections that CloudWatch doesn't attribute
to a table: the per-connection limit, and invalid `EXECUTE` and `PREPARE` statements.

## Availability SLO

The Keyspaces application has the default [availability SLO](/inspections/slo#availability). Coroot calculates it
from the eBPF data of the clients. When no client has request data, for example because all clients use a TLS library
that eBPF can't read (see [Amazon Keyspaces](/databases/keyspaces#clients-that-ebpf-cant-read)), Coroot calculates
it from CloudWatch instead:

* **failed requests** = the sum over the tables of the larger of `UserErrors` and the read and write throttle events,
  plus the larger of the account's `EXECUTE` `UserErrors` and the per-connection limit rejections, plus the system
  errors;
* **all requests** = the successful requests plus the failed requests.

Invalid `PREPARE` statements are not counted: they are mistakes in the client code, not failed requests.
