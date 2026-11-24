---
sidebar_position: 5.5
---

# AWS

The AWS integration lets Coroot discover Amazon RDS instances and ElastiCache nodes and collect their telemetry.
It can also monitor [Amazon Keyspaces](#amazon-keyspaces) tables. Keyspaces monitoring is off by default.
Discovered instances appear in the Service Map as separate applications, linked to the services that connect to them
through the eBPF-based metrics collected on the client side.

The integration adds:

* **Instance inventory and status**: engine, version, instance type, storage, Multi-AZ, read replicas, backup retention
* **OS-level metrics** from [RDS Enhanced Monitoring](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_Monitoring.OS.html): CPU, memory, disk I/O, network
* **Database logs** of RDS Postgres and MySQL instances (including Aurora), read through the RDS API: shipped to Coroot and grouped by message pattern
* **Cost estimates** for RDS and ElastiCache instances in the [Costs](/costs/overview) report
* **Amazon Keyspaces tables**: capacity, latency, requests, size and rejected requests, read from CloudWatch

Database internals (query statistics, locks, replication) are collected separately, see
[Database credentials](#database-credentials).

All AWS API calls are made by the [cluster-agent](/installation/architecture), not by the Coroot server. The agent polls
the RDS and ElastiCache APIs once a minute, so a new instance shows up within about a minute of being created.

The recommended setup uses the IAM role of the cluster-agent pod and declares the settings in the Coroot custom
resource, see [Configuration as code](#configuration-as-code). Alternatively, go to the **Project Settings**, open
**Cloud integrations**, expand the AWS settings and fill in the form described below. Either way, the page shows the
discovery status, the last errors reported by the agent, and the list of discovered instances.

## IAM permissions

Whatever identity the agent uses, it needs the following read-only permissions: describing RDS and ElastiCache instances
and their tags, reading RDS log files, and reading the `RDSOSMetrics` CloudWatch Logs group written by Enhanced Monitoring.
The last two statements are for [Amazon Keyspaces](#amazon-keyspaces). You can leave them out when Keyspaces monitoring is off.
The Keyspaces control-plane API needs `cassandra:Select` on the keyspaces and tables, so this statement also allows
reading table data. The agent reads only the table list and the `system.peers` table.

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "rds:DescribeDBInstances",
                "rds:DescribeDBLogFiles",
                "rds:DownloadDBLogFilePortion",
                "rds:ListTagsForResource",
                "elasticache:DescribeCacheClusters",
                "elasticache:ListTagsForResource"
            ],
            "Resource": "*"
        },
        {
            "Effect": "Allow",
            "Action": [
                "logs:GetLogEvents"
            ],
            "Resource": "arn:aws:logs:*:*:log-group:RDSOSMetrics:log-stream:*"
        },
        {
            "Effect": "Allow",
            "Action": [
                "cassandra:Select"
            ],
            "Resource": "arn:aws:cassandra:*:*:/keyspace/*"
        },
        {
            "Effect": "Allow",
            "Action": [
                "application-autoscaling:DescribeScalableTargets",
                "application-autoscaling:DescribeScalingPolicies",
                "cloudwatch:GetMetricData",
                "cloudwatch:ListMetrics"
            ],
            "Resource": "*"
        }
    ]
}
```

## Credentials

The agent supports three ways of authenticating to AWS. Leave the **Access Key ID** and **Secret Access Key** fields
empty for the first two.

| Method | Where the agent runs | Credential lifetime |
|--------|----------------------|---------------------|
| **IAM role of the pod** (EKS Pod Identity or IRSA) | Amazon EKS | Temporary, rotated automatically |
| **EC2 instance profile** | An EC2 instance outside EKS | Temporary, rotated automatically |
| **Static access key** | Anywhere, including outside AWS | Long-lived |

With the keys empty the agent uses the AWS SDK's default credential chain: environment variables, EKS Pod Identity,
IRSA, shared config files, and finally the EC2 instance profile. Binding an IAM role to the cluster-agent on EKS is
described step by step in the [Monitoring Amazon RDS and ElastiCache from EKS](/guides/aws-eks-rds) guide.

For a static key, create an [IAM user](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_users_create.html) with
programmatic access, attach the policy above, and enter its Access Key ID and Secret Access Key in the form. Coroot
stores the key in the project settings and passes it to the cluster-agent along with the rest of the configuration.

## Region

Coroot discovers RDS and ElastiCache instances and Keyspaces tables in a single region. Leave the **Region** field empty to use the region
the cluster-agent runs in: the agent takes it from the `AWS_REGION` environment variable, then from the
`topology.kubernetes.io/region` label of the Kubernetes nodes, and finally from the EC2 instance metadata service.
Set the field explicitly to monitor instances in a different region than the one the agent runs in.

## Tag filters

By default every RDS instance and ElastiCache cluster in the region is discovered. Use **RDS tag filters** and
**ElastiCache tag filters** to restrict discovery to instances whose tags match `tag=value` pairs, separated by
commas. [Glob patterns](https://en.wikipedia.org/wiki/Glob_(programming)) are supported in the value part:

```
team=payments,env=prod*
```

An instance is discovered only if all listed tags match.

**Keyspaces tag filters** work the same way, but they match the tags of a keyspace, not of a table: tables don't inherit
the tags of their keyspace. Coroot monitors all tables of a keyspace whose tags match.

## Enhanced Monitoring

OS-level metrics come from the `RDSOSMetrics` CloudWatch Logs group, which exists only for instances with
Enhanced Monitoring enabled. Enable it with a granularity of 60 seconds or lower on each instance you want these
metrics for; instances without it still get inventory, status, and log metrics.

## Logs

The cluster-agent tails the log files of RDS instances with the `postgres`, `aurora-postgresql`, `mysql`, `mariadb` and
`aurora-mysql` engines through the RDS API and forwards every message to Coroot as an OpenTelemetry log record, the
same way coroot-node-agent forwards container logs. All log files the RDS API lists for the instance are read: for
Postgres that is the server log, for MySQL the error log plus the slow query and general logs when they are written
to files (`log_output=FILE` in the parameter group). Messages
are grouped by automatically extracted patterns, and multi-line messages are joined. The records carry the
`pattern.hash` attribute and `service.name` set to `/aws/rds/<region>/<DBInstanceIdentifier>`, which Coroot uses to
show them in the **Logs** tab of the RDS application. Log files are polled every 30 seconds, so a record's timestamp
can lag the time in the log line by up to that much.

Log collection needs the `rds:DescribeDBLogFiles` and `rds:DownloadDBLogFilePortion` permissions from the policy
above. Forwarding can be disabled with the cluster-agent's `--collect-aws-logs=false` flag, in which case only the
pattern-based `aws_rds_log_messages_total` metric is collected. ElastiCache logs are not collected.

## Amazon Keyspaces

Amazon Keyspaces has no hosts, so the cluster-agent can't read metrics from the database itself. Instead, it reads the
table inventory from the Keyspaces API and the server-side metrics from CloudWatch. The eBPF-based metrics of the
clients complete the picture, see [Amazon Keyspaces](/databases/keyspaces).

To enable it, select **Monitor Amazon Keyspaces** in the AWS settings, or set `keyspacesEnabled: true` in the
[configuration as code](#configuration-as-code). The agent needs the Keyspaces statements of the
[IAM policy](#iam-permissions).

When it is enabled, the agent does these steps:

* **Every minute**, it lists the keyspaces of the account and their tables. It gets the account ID from STS. The
  system keyspaces (`system`, `system_schema`, `system_schema_mcs`, `system_multiregion_info`) are skipped. Every 5 minutes, it reads
  the capacity mode, the provisioned read and write capacity, and the auto scaling settings of each table.
* **Every 5 minutes**, it finds the endpoints that clients connect to on port 9142, so that Coroot can link the
  clients to Keyspaces. It always adds the regional names `cassandra.<region>.amazonaws.com` and
  `cassandra.<region>.api.aws`, and it resolves the IPv4 addresses of `cassandra.<region>.amazonaws.com`.
  * When DNS returns only private IP addresses, the name points to a
    [VPC interface endpoint](https://docs.aws.amazon.com/keyspaces/latest/devguide/vpc-endpoints.html) with private
    DNS in the VPC of the agent. The agent uses these addresses.
  * Otherwise, DNS returns one address of a pool of public addresses at a time. The agent connects to one of them,
    reads the `system.peers` table, and then reads `system.peers` on each peer it found. The connections use TLS and
    the AWS SigV4 authentication with the agent's credentials.
* **It polls CloudWatch** (the `AWS/Cassandra` namespace):

  | Data | Interval |
  |------|----------|
  | Consumed read and write capacity | 1 minute |
  | p99 latency and number of successful requests, for the `SELECT`, `INSERT`, `UPDATE`, `DELETE` and `BATCH` operations that have traffic | 5 minutes |
  | Table size | 1 hour |
  | Rejected requests: hot partitions, throttling, invalid requests, system errors and the per-connection limit | 1 minute |

  The agent reads only the error series that CloudWatch reports as active in the last 3 hours. Every value ends
  2 to 3 minutes in the past, because CloudWatch publishes data late. Rejected requests are counted over 5 minutes.

Coroot shows each AWS account and region as one application, named `<account ID>-<region>`, with the
[Keyspaces inspection](/inspections/keyspaces). The application has no nodes, so it has no CPU or memory data.

Limitations:

* The China regions (`amazonaws.com.cn`) are not supported.
* The account-level CloudWatch series have no table, so the tag filters don't apply to them.
* When the agent resolves the regional name to public addresses, the Keyspaces application raises the
  [Publicly Exposed Databases](/risks/overview#publicly-exposed-databases) risk. A VPC interface endpoint with private
  DNS in the VPC of the agent clears it.

## Database credentials

The AWS API provides instance status and OS metrics. Query statistics, locks, and replication state come from the
database itself, so the cluster-agent connects to each instance directly:

1. Allow inbound connections from the cluster-agent to the database port in the instance's security group.
2. Create a monitoring user as described in [Postgres](/databases/postgres) or [MySQL](/databases/mysql).
3. Open the instance's application in Coroot, go to the **Postgres** or **MySQL** tab, click **Configure**, and enter
   the credentials. RDS Postgres 15 and later rejects unencrypted connections by default, so set **sslmode** to
   `require`.

ElastiCache Redis, Valkey and Memcached nodes need no credentials unless AUTH is enabled. Enable collection the same way,
from the **Redis** or **Memcached** tab of the node's application.

An `rds:` entry in the [configuration as code](#configuration-as-code) covers the instance and its read replicas: the
same monitoring user exists on the replicas, so they are monitored with the primary's credentials without being
declared separately. Replicas are subject to the [tag filters](#tag-filters) like any other instance.

The `rdsadmin` database that Amazon RDS creates on every Postgres instance is used by the service itself, so it is
excluded from monitoring by default: its maintenance connections and queries don't show up among yours, see
[`--exclude-databases`](/configuration/coroot-cluster-agent).

## Configuration as code

When Coroot is deployed by the [Kubernetes Operator](/installation/k8s-operator), the AWS integration and the database
credentials can be declared in the Coroot custom resource instead of the UI. Secrets are referenced, never stored in
the resource, and the settings take precedence over the UI:

```yaml
spec:
  clusterAgent:
    aws:
      rdsTagFilters: {team: payments}
      keyspacesEnabled: true
      keyspacesTagFilters: {team: payments}
    databases:
      - type: postgres
        rds: my-db
        credentials:
          usernameSecret: {name: my-db-coroot, key: username}
          passwordSecret: {name: my-db-coroot, key: password}
        params: {sslmode: require}
      - type: redis
        elasticache: my-cache
```

The operator renders these into the cluster-agent's [configuration file](/configuration/coroot-cluster-agent#configuration-file),
which can also be used directly for installations without the operator.

## Troubleshooting

The cluster-agent logs the effective region, the credential source, and the IAM identity it runs as whenever the
integration is configured or changed, followed by a discovery summary:

```
AWS integration: region=us-east-1, credentials=CredentialsEndpointProvider, identity=arn:aws:sts::123456789012:assumed-role/coroot-cluster-agent/...
AWS discovery (region=us-east-1): 3 RDS instances, 2 ElastiCache nodes, 4 Keyspaces tables
```

The `Keyspaces tables` count is shown only when Keyspaces monitoring is enabled.

`CredentialsEndpointProvider` means EKS Pod Identity, `WebIdentityCredentials` means IRSA, `EC2RoleProvider` means the
instance profile, and `StaticCredentials` means the key from the integration settings. Discovery errors are shown on
the **Cloud integrations** page and exposed as the `aws_discovery_error` metric. The full list of collected metrics is in the
[cluster-agent metrics reference](/metrics/cluster-agent#aws).
