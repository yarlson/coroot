package constructor

import (
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

func keyspacesKey(id string) string { return "keyspaces/" + id }

func (c *Constructor) loadKeyspacesMetadata(w *model.World, metrics map[string][]*model.MetricValues, cloudInstancesById map[string]*model.Instance, project *db.Project) {
	for _, m := range metrics["aws_keyspaces_info"] {
		id, accountId, region := m.Labels["keyspaces_id"], m.Labels["account_id"], m.Labels["region"]
		if id == "" || accountId == "" || region == "" {
			continue
		}
		instance := cloudInstancesById[keyspacesKey(id)]
		if instance == nil {
			name := accountId + "-" + region
			appId := c.newApplicationId(project.ClusterId(), "", model.ApplicationKindKeyspaces, name)
			instance = w.GetOrCreateApplication(appId, false).GetOrCreateInstance(name, nil)
			instance.Keyspaces = model.NewKeyspaces(id, accountId, region)
			cloudInstancesById[keyspacesKey(id)] = instance
		}
		instance.Keyspaces.LifeSpan = merge(instance.Keyspaces.LifeSpan, m.Values, timeseries.Any)
	}
	for _, m := range metrics["aws_keyspaces_endpoint"] {
		instance := cloudInstancesById[keyspacesKey(m.Labels["keyspaces_id"])]
		if instance == nil || m.Labels["host"] == "" || m.Labels["port"] == "" {
			continue
		}
		instance.TcpListens[model.Listen{IP: m.Labels["host"], Port: m.Labels["port"]}] = true
	}
}

func loadKeyspaces(metrics map[string][]*model.MetricValues, cloudInstancesById map[string]*model.Instance) {
	keyspaces := func(m *model.MetricValues) *model.Keyspaces {
		if instance := cloudInstancesById[keyspacesKey(m.Labels["keyspaces_id"])]; instance != nil {
			return instance.Keyspaces
		}
		return nil
	}
	table := func(m *model.MetricValues) *model.KeyspacesTable {
		k := keyspaces(m)
		if k == nil || m.Labels["keyspace"] == "" || m.Labels["table"] == "" {
			return nil
		}
		return k.GetOrCreateTable(model.KeyspacesTableKey{Keyspace: m.Labels["keyspace"], Table: m.Labels["table"]})
	}

	for _, m := range metrics["aws_keyspaces_table_info"] {
		if t := table(m); t != nil {
			t.CapacityMode.Update(m.Values, m.Labels["capacity_mode"])
			t.AutoScaling.Update(m.Values, m.Labels["auto_scaling"])
		}
	}
	for _, m := range metrics["aws_keyspaces_table_provisioned_capacity_units"] {
		if t := table(m); t != nil {
			switch m.Labels["operation"] {
			case model.KeyspacesCapacityRead:
				t.ProvisionedRead = merge(t.ProvisionedRead, m.Values, timeseries.Any)
			case model.KeyspacesCapacityWrite:
				t.ProvisionedWrite = merge(t.ProvisionedWrite, m.Values, timeseries.Any)
			}
		}
	}
	for _, m := range metrics["aws_keyspaces_table_consumed_capacity_units"] {
		if t := table(m); t != nil {
			switch m.Labels["operation"] {
			case model.KeyspacesCapacityRead:
				t.ConsumedRead = merge(t.ConsumedRead, m.Values, timeseries.Any)
			case model.KeyspacesCapacityWrite:
				t.ConsumedWrite = merge(t.ConsumedWrite, m.Values, timeseries.Any)
			}
		}
	}
	for _, m := range metrics["aws_keyspaces_table_size_bytes"] {
		if t := table(m); t != nil {
			t.SizeBytes = merge(t.SizeBytes, m.Values, timeseries.Any)
		}
	}
	for _, m := range metrics["aws_keyspaces_table_latency_seconds"] {
		if t := table(m); t != nil {
			op := m.Labels["operation"]
			t.Latency[op] = merge(t.Latency[op], m.Values, timeseries.Any)
		}
	}
	for _, m := range metrics["aws_keyspaces_table_requests"] {
		if t := table(m); t != nil {
			op := m.Labels["operation"]
			t.Requests[op] = merge(t.Requests[op], m.Values, timeseries.Any)
		}
	}
	for _, m := range metrics["aws_keyspaces_table_errors"] {
		if t := table(m); t != nil {
			typ := m.Labels["type"]
			t.Errors[typ] = merge(t.Errors[typ], m.Values, timeseries.Any)
		}
	}
	for _, m := range metrics["aws_keyspaces_account_errors"] {
		if k := keyspaces(m); k != nil {
			key := model.KeyspacesAccountErrorKey{Type: m.Labels["type"], Operation: m.Labels["operation"]}
			k.AccountErrors[key] = merge(k.AccountErrors[key], m.Values, timeseries.Any)
		}
	}
}
