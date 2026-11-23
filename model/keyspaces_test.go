package model

import (
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/stretchr/testify/assert"
)

func TestExternalCassandraTyping(t *testing.T) {
	ctx := timeseries.NewContext(1000, 1060, 15)
	ts := timeseries.NewWithData(ctx.From, ctx.Step, []float32{1, 1, 1, 1, 1})

	// ProtocolCassandra maps to ApplicationTypeCassandra
	assert.Equal(t, ApplicationTypeCassandra, ProtocolCassandra.ToApplicationType())

	// ExternalService with port 9142 gets ApplicationTypeCassandra
	ext := NewApplication(NewApplicationId(ClusterIdExternal, "external", ApplicationKindExternalService, "external-cassandra"))
	client := NewApplication(NewApplicationId("c1", "default", ApplicationKindDeployment, "api"))
	eps := utils.NewStringSet()
	eps.Add("18.141.148.46:9142")
	conn := &AppToAppConnection{
		Application:       client,
		RemoteApplication: ext,
		Endpoints:         eps,
		RequestsCount: map[Protocol]map[string]*timeseries.TimeSeries{
			ProtocolCassandra: {"ok": ts},
		},
	}
	ext.Downstreams[client.Id] = conn

	types := ext.ApplicationTypes()
	assert.True(t, types[ApplicationTypeCassandra])
	assert.True(t, ext.IsDatabase())
	assert.Equal(t, ApplicationTypeCassandra, ext.ApplicationType())

	// ExternalService with port 9042 (no L7) also maps to ApplicationTypeCassandra
	extPortOnly := NewApplication(NewApplicationId(ClusterIdExternal, "external", ApplicationKindExternalService, "cass-9042"))
	eps2 := utils.NewStringSet()
	eps2.Add("10.0.0.5:9042")
	extPortOnly.Downstreams[client.Id] = &AppToAppConnection{
		Application:       client,
		RemoteApplication: extPortOnly,
		Endpoints:         eps2,
	}
	assert.True(t, extPortOnly.ApplicationTypes()[ApplicationTypeCassandra])
}

func TestKeyspacesModelAndDefaultSLO(t *testing.T) {
	ctx := timeseries.NewContext(1000, 1060, 15)
	ts := timeseries.NewWithData(ctx.From, ctx.Step, []float32{1, 1, 1, 1, 1})

	ksAppId := NewApplicationId("c1", "", ApplicationKindKeyspaces, "006242033817-ap-southeast-1")
	app := NewApplication(ksAppId)
	inst := app.GetOrCreateInstance("006242033817-ap-southeast-1", nil)
	inst.Keyspaces = NewKeyspaces("006242033817/ap-southeast-1")
	inst.Keyspaces.LifeSpan = ts

	assert.Nil(t, inst.Node)
	assert.True(t, inst.IsManagedDatabase())
	assert.True(t, inst.IsUp())
	assert.True(t, app.IsDatabase())
	assert.True(t, app.IsKeyspaces())
	assert.Equal(t, ApplicationTypeCassandra, app.ApplicationType())
	assert.Equal(t, "cassandra (Keyspaces)", app.Labels()["db"])

	// Default SLO: ExternalService gets 0%, Keyspaces inherits default 99%
	cc := CheckConfigs{}
	extId := NewApplicationId(ClusterIdExternal, "external", ApplicationKindExternalService, "external-cassandra")
	extAvail, _ := cc.GetAvailability(extId)
	assert.Equal(t, float32(0), extAvail.ObjectivePercentage)

	ksAvail, _ := cc.GetAvailability(ksAppId)
	assert.Equal(t, float32(99), ksAvail.ObjectivePercentage)

	ksLat, _ := cc.GetLatency(ksAppId, ApplicationCategoryApplication)
	assert.Equal(t, float32(99), ksLat.ObjectivePercentage)

	// Phase 1 regression: GetDefaultInstrumentation(cassandra) returns nil (must be nil-checked in API handler)
	assert.Nil(t, GetDefaultInstrumentation(ApplicationTypeCassandra))
}
