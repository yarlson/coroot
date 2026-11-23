package constructor

import (
	"testing"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyspacesMetadataLinkingAndMulticlusterMerge(t *testing.T) {
	ctx := timeseries.NewContext(1000, 1060, 15)
	ones := timeseries.NewWithData(ctx.From, ctx.Step, []float32{1, 1, 1, 1, 1})
	consW := timeseries.NewWithData(ctx.From, ctx.Step, []float32{4.5, 4.5, 4.5, 4.5, 4.5})
	provW := timeseries.NewWithData(ctx.From, ctx.Step, []float32{5, 5, 5, 5, 5})
	reqWin := timeseries.NewWithData(ctx.From, ctx.Step, []float32{1800, 1800, 1800, 1800, 1800})
	writeThr := timeseries.NewWithData(ctx.From, ctx.Step, []float32{200, 200, 200, 200, 200})
	userErr := timeseries.NewWithData(ctx.From, ctx.Step, []float32{200, 200, 200, 200, 200})
	prepErr := timeseries.NewWithData(ctx.From, ctx.Step, []float32{50, 50, 50, 50, 50})

	project := &db.Project{Id: "p-ks", Name: "keyspaces"}
	wKS := model.NewWorld(ctx.From, ctx.To, ctx.Step, ctx.Step)
	cloudInstances := map[string]*model.Instance{}

	metrics := map[string][]*model.MetricValues{
		"aws_keyspaces_info": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "account": "006242033817", "region": "ap-southeast-1"}, Values: ones},
		},
		"aws_keyspaces_endpoint_info": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "addr": "cassandra.ap-southeast-1.amazonaws.com", "port": "9142", "kind": "public"}, Values: ones},
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "addr": "18.141.148.46", "port": "9142", "kind": "peer"}, Values: ones},
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "addr": "10.78.1.163", "port": "9142", "kind": "vpc"}, Values: ones},
		},
		"aws_keyspaces_table_info": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov", "capacity_mode": "PROVISIONED", "status": "ACTIVE"}, Values: ones},
		},
		"aws_keyspaces_provisioned_capacity_units": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov", "type": "write"}, Values: provW},
		},
		"aws_keyspaces_consumed_capacity_units_per_second": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov", "type": "write"}, Values: consW},
		},
		"aws_keyspaces_requests_window": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov", "operation": "INSERT"}, Values: reqWin},
		},
		"aws_keyspaces_throttle_events": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov", "type": "write"}, Values: writeThr},
		},
		"aws_keyspaces_user_errors": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "keyspace": "keyspaces_val", "table": "t_prov"}, Values: userErr},
		},
		"aws_keyspaces_account_user_errors": {
			{Labels: model.Labels{"keyspaces_id": "006242033817/ap-southeast-1", "operation": "PREPARE"}, Values: prepErr},
		},
	}

	c := &Constructor{}
	c.loadKeyspacesMetadata(wKS, metrics, cloudInstances, project)
	c.loadKeyspaces(wKS, metrics, cloudInstances)

	ksAppId := model.NewApplicationId("p-ks", "", model.ApplicationKindKeyspaces, "006242033817-ap-southeast-1")
	ksApp := wKS.GetApplication(ksAppId)
	require.NotNil(t, ksApp)
	require.Len(t, ksApp.Instances, 1)

	inst := ksApp.Instances[0]
	assert.Nil(t, inst.Node, "Keyspaces instance must be node-less")
	assert.True(t, inst.TcpListens[model.Listen{IP: "cassandra.ap-southeast-1.amazonaws.com", Port: "9142"}])
	assert.True(t, inst.TcpListens[model.Listen{IP: "18.141.148.46", Port: "9142"}])
	assert.True(t, inst.TcpListens[model.Listen{IP: "10.78.1.163", Port: "9142"}])

	tbl := inst.Keyspaces.Tables[model.KeyspacesTableKey{Keyspace: "keyspaces_val", Table: "t_prov"}]
	require.NotNil(t, tbl)
	assert.Equal(t, "PROVISIONED", tbl.CapacityMode.Value())
	assert.Equal(t, float32(4.5), tbl.ConsumedWrite.Last())
	assert.Equal(t, float32(5), tbl.ProvisionedWrite.Last())

	// CloudWatch Availability SLO fallback: good=1800, failed=200 (not 400!), PREPARE=50 excluded -> total=2000/300s, failed=200/300s
	sli := keyspacesAvailability(ksApp)
	assert.InDelta(t, 2000.0/300.0, float64(sli.total.Last()), 0.01)
	assert.InDelta(t, 200.0/300.0, float64(sli.failed.Last()), 0.01)

	// Multi-cluster merge: client cluster sees ExternalService on peer IP 18.141.148.46:9142;
	// mergeWorlds rewires it straight to ksApp because inst.Node == nil.
	wClient := model.NewWorld(ctx.From, ctx.To, ctx.Step, ctx.Step)
	clientApp := wClient.GetOrCreateApplication(model.NewApplicationId("p-client", "default", model.ApplicationKindDeployment, "orders-api"), false)
	extApp := wClient.GetOrCreateApplication(model.NewApplicationId(model.ClusterIdExternal, "external", model.ApplicationKindExternalService, "external-cassandra"), false)
	eps := utils.NewStringSet()
	eps.Add("18.141.148.46:9142")
	conn := &model.AppToAppConnection{
		Application:       clientApp,
		RemoteApplication: extApp,
		Endpoints:         eps,
	}
	clientApp.Upstreams[extApp.Id] = conn
	extApp.Downstreams[clientApp.Id] = conn

	merged := mergeWorlds([]*model.World{wClient, wKS})
	assert.Nil(t, merged.GetApplication(extApp.Id), "ExternalService should be replaced by Keyspaces app")
	assert.Equal(t, ksApp, clientApp.Upstreams[extApp.Id].RemoteApplication)
	assert.Contains(t, ksApp.Downstreams, clientApp.Id)
}
