package constructor

import (
	"strings"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

func keyspacesKey(id string) string { return "keyspaces/" + id }

func (c *Constructor) loadKeyspacesMetadata(w *model.World, metrics map[string][]*model.MetricValues, cloudInstancesById map[string]*model.Instance, project *db.Project) {
	for _, m := range metrics["aws_keyspaces_info"] {
		ksID := m.Labels["keyspaces_id"]
		if ksID == "" {
			continue
		}
		account := m.Labels["account"]
		region := m.Labels["region"]
		if account == "" || region == "" {
			parts := strings.SplitN(ksID, "/", 2)
			if len(parts) == 2 {
				if account == "" {
					account = parts[0]
				}
				if region == "" {
					region = parts[1]
				}
			}
		}
		if account == "" || region == "" {
			continue
		}

		instance := cloudInstancesById[keyspacesKey(ksID)]
		if instance == nil {
			appName := account + "-" + region
			appId := c.newApplicationId(project.ClusterId(), "", model.ApplicationKindKeyspaces, appName)
			// Single node-less instance (Instance.Node == nil) avoids empty host views and
			// keeps multi-cluster sock2app remapping working cleanly.
			instance = w.GetOrCreateApplication(appId, false).GetOrCreateInstance(appName, nil)
			cloudInstancesById[keyspacesKey(ksID)] = instance
			instance.Keyspaces = model.NewKeyspaces(ksID)
		}
		instance.Keyspaces.Account.Update(m.Values, account)
		instance.Keyspaces.Region.Update(m.Values, region)
		instance.Keyspaces.Status.Update(m.Values, "active")
		instance.Keyspaces.LifeSpan = merge(instance.Keyspaces.LifeSpan, m.Values, timeseries.Any)
	}

	for _, m := range metrics["aws_keyspaces_endpoint_info"] {
		ksID := m.Labels["keyspaces_id"]
		addr := m.Labels["addr"]
		port := m.Labels["port"]
		if ksID == "" || addr == "" {
			continue
		}
		if port == "" {
			port = "9142"
		}
		instance := cloudInstancesById[keyspacesKey(ksID)]
		if instance == nil || instance.Keyspaces == nil {
			continue
		}
		instance.TcpListens[model.Listen{IP: addr, Port: port}] = true
		instance.Keyspaces.Endpoints[addr] = m.Labels["kind"]
	}

	for _, m := range metrics["aws_keyspaces_table_info"] {
		ksID := m.Labels["keyspaces_id"]
		ks := m.Labels["keyspace"]
		tbl := m.Labels["table"]
		if ksID == "" || ks == "" || tbl == "" {
			continue
		}
		instance := cloudInstancesById[keyspacesKey(ksID)]
		if instance == nil || instance.Keyspaces == nil {
			continue
		}
		t := instance.Keyspaces.GetOrCreateTable(ks, tbl)
		t.CapacityMode.Update(m.Values, m.Labels["capacity_mode"])
		t.Status.Update(m.Values, m.Labels["status"])
	}
}

func (c *Constructor) loadKeyspaces(w *model.World, metrics map[string][]*model.MetricValues, cloudInstancesById map[string]*model.Instance) {
	for _, q := range QUERIES {
		if !strings.HasPrefix(q.Name, "aws_keyspaces_") ||
			q.Name == "aws_keyspaces_info" ||
			q.Name == "aws_keyspaces_endpoint_info" ||
			q.Name == "aws_keyspaces_table_info" {
			continue
		}
		for _, m := range metrics[q.Name] {
			ksID := m.Labels["keyspaces_id"]
			if ksID == "" {
				continue
			}
			instance := cloudInstancesById[keyspacesKey(ksID)]
			if instance == nil || instance.Keyspaces == nil {
				continue
			}
			ks := instance.Keyspaces

			switch q.Name {
			case "aws_keyspaces_account_user_errors":
				op := m.Labels["operation"]
				if op != "" {
					ks.AccountUserErrors[op] = merge(ks.AccountUserErrors[op], m.Values, timeseries.Any)
				}
				continue
			case "aws_keyspaces_per_connection_limit_events":
				ks.PerConnectionLimitEvents = merge(ks.PerConnectionLimitEvents, m.Values, timeseries.Any)
				continue
			}

			keyspaceName := m.Labels["keyspace"]
			tableName := m.Labels["table"]
			if keyspaceName == "" || tableName == "" {
				continue
			}
			t := ks.GetOrCreateTable(keyspaceName, tableName)

			switch q.Name {
			case "aws_keyspaces_provisioned_capacity_units":
				switch m.Labels["type"] {
				case "read":
					t.ProvisionedRead = merge(t.ProvisionedRead, m.Values, timeseries.Any)
				case "write":
					t.ProvisionedWrite = merge(t.ProvisionedWrite, m.Values, timeseries.Any)
				}
			case "aws_keyspaces_warm_throughput_units":
				switch m.Labels["type"] {
				case "read":
					t.WarmRead = merge(t.WarmRead, m.Values, timeseries.Any)
				case "write":
					t.WarmWrite = merge(t.WarmWrite, m.Values, timeseries.Any)
				}
			case "aws_keyspaces_consumed_capacity_units_per_second":
				switch m.Labels["type"] {
				case "read":
					t.ConsumedRead = merge(t.ConsumedRead, m.Values, timeseries.Any)
				case "write":
					t.ConsumedWrite = merge(t.ConsumedWrite, m.Values, timeseries.Any)
				}
			case "aws_keyspaces_table_size_bytes":
				t.TableSizeBytes = merge(t.TableSizeBytes, m.Values, timeseries.Any)
			case "aws_keyspaces_requests_window":
				op := m.Labels["operation"]
				if op != "" {
					t.RequestsWindow[op] = merge(t.RequestsWindow[op], m.Values, timeseries.Any)
				}
			case "aws_keyspaces_request_latency_seconds":
				op := m.Labels["operation"]
				if op != "" && m.Labels["stat"] == "p99" {
					t.LatencyP99[op] = merge(t.LatencyP99[op], m.Values, timeseries.Any)
				}
			case "aws_keyspaces_throttle_events":
				switch m.Labels["type"] {
				case "read":
					t.ReadThrottleEvents = merge(t.ReadThrottleEvents, m.Values, timeseries.Any)
				case "write":
					t.WriteThrottleEvents = merge(t.WriteThrottleEvents, m.Values, timeseries.Any)
				}
			case "aws_keyspaces_partition_limit_events":
				t.PartitionLimitEvents = merge(t.PartitionLimitEvents, m.Values, timeseries.Any)
			case "aws_keyspaces_user_errors":
				t.UserErrors = merge(t.UserErrors, m.Values, timeseries.Any)
			case "aws_keyspaces_system_errors":
				t.SystemErrors = merge(t.SystemErrors, m.Values, timeseries.Any)
			}
		}
	}
}
