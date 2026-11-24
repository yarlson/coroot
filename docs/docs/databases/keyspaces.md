---
sidebar_position: 6
---

# Amazon Keyspaces

Amazon Keyspaces is a managed, Cassandra-compatible database. It has no hosts that you can run an agent on or read
metrics from. Coroot monitors it from two sides:

* **The clients.** The node agent uses eBPF to see the Cassandra connections and queries of your applications, as it
  does for any other database.
* **The AWS side.** The cluster-agent reads the tables from the Keyspaces API and their metrics from CloudWatch:
  consumed capacity, latency, requests, size, and rejected requests.

The cluster-agent doesn't need database credentials. It uses the IAM permissions of the
[AWS integration](/configuration/aws#iam-permissions).

## Enabling

Keyspaces monitoring is part of the AWS integration and is off by default. To enable it, select
**Monitor Amazon Keyspaces** in the AWS integration settings, or set `keyspacesEnabled: true` in the Coroot custom
resource. See [Amazon Keyspaces](/configuration/aws#amazon-keyspaces) for the settings, the tag filters, and what the
agent polls.

Coroot shows each AWS account and region as one application, named `<account ID>-<region>`. The connections of your
applications to Keyspaces link to it instead of to an external service.

## Endpoints

Clients connect to Keyspaces on port 9142 through one of two kinds of endpoints:

* **A VPC interface endpoint with private DNS.** The regional name resolves to private IP addresses in your VPC. The
  cluster-agent resolves the name and links the connections to these addresses. The traffic stays in your network.
* **The public regional endpoint.** DNS returns one address of a pool of public addresses at a time, and the drivers
  find the others in the `system.peers` table. The cluster-agent connects to Keyspaces with TLS and SigV4
  authentication, reads `system.peers` on the first address and on each peer it finds, and links the connections to
  all these addresses.

The cluster-agent sees only the endpoint of its own VPC. When it resolves the regional name to public addresses, the
Keyspaces application raises the [Publicly Exposed Databases](/risks/overview#publicly-exposed-databases) risk.

## Multiple clusters

Enable Keyspaces monitoring in one project for each account and region. Every cluster-agent that has it enabled polls
CloudWatch independently. When the clients run in other clusters, combine their projects with the
monitoring project into a multi-cluster project: the client connections then link to the Keyspaces application across
the clusters.

This has two limits:

* Coroot links only the connections to the addresses that the monitoring cluster-agent found. Clients that use the
  public endpoint, or the same VPC interface endpoint as that agent, are linked. Clients that use an interface
  endpoint in another VPC stay external services.
* Coroot calculates the availability SLO in the project that monitors Keyspaces, before it combines the projects. So
  the SLO uses only the clients in that project.

## Clients that eBPF can't read

Keyspaces accepts only TLS connections. The node agent reads the requests of most TLS libraries, but not of these clients:

* Java clients, unless the node agent runs with [`--enable-java-tls`](/configuration/coroot-node-agent);
* Java clients that use the asynchronous `SSLEngine`, such as drivers built on Netty;
* Node.js clients.

Coroot still sees the connections of such clients, but not their requests. When Coroot has no request data for a
Keyspaces application at all, it calculates the availability SLO of the application from CloudWatch instead, see the
[Keyspaces inspection](/inspections/keyspaces#availability-slo).

## Inspection

The [Keyspaces inspection](/inspections/keyspaces) checks for hot partitions, throttling, provisioned capacity, the
per-connection limit, system errors and invalid requests. Four of these checks have
[built-in alerting rules](/alerting/alerts#built-in-rules).
