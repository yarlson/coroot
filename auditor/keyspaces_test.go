package auditor

import (
	"testing"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyKeyspacesRejections_SpikeAccounting(t *testing.T) {
	// 1. Provisioned write throttle: 596 WriteThrottle, 596 UserErrors -> 596 write cap throttle, 0 net user errors
	c := classifyKeyspacesTableRejections(0, 596, 0, 596, 0)
	assert.Equal(t, float32(0), c.HotPartition)
	assert.Equal(t, float32(0), c.ReadCapThrottle)
	assert.Equal(t, float32(596), c.WriteCapThrottle)
	assert.Equal(t, float32(0), c.NetClientUserErrors)

	// 2. Provisioned read throttle: 1262 ReadThrottle, 1262 UserErrors -> 1262 read cap throttle, 0 net user errors
	c = classifyKeyspacesTableRejections(1262, 0, 0, 1262, 0)
	assert.Equal(t, float32(1262), c.ReadCapThrottle)
	assert.Equal(t, float32(0), c.WriteCapThrottle)
	assert.Equal(t, float32(0), c.NetClientUserErrors)

	// 3. Hot partition (triple-counted): 48698 HotPartition, 48698 WriteThrottle, 48698 UserErrors -> ONLY HotPartition
	c = classifyKeyspacesTableRejections(0, 48698, 48698, 48698, 0)
	assert.Equal(t, float32(48698), c.HotPartition)
	assert.Equal(t, float32(0), c.WriteCapThrottle)
	assert.Equal(t, float32(0), c.NetClientUserErrors)

	// 4. Per-connection limit (double-counted): 144839 PerConn, 144839 EXECUTE UserErrors -> 144839 PerConn, 0 net user errors
	ac := classifyKeyspacesAccountRejections(144839, 144839, 0)
	assert.Equal(t, float32(144839), ac.PerConnectionLimit)
	assert.Equal(t, float32(0), ac.NetClientUserErrors)

	// 5. Invalid PREPARE query: 50 PREPARE UserErrors -> 50 net account user errors
	ac = classifyKeyspacesAccountRejections(144839, 144839, 50)
	assert.Equal(t, float32(144839), ac.PerConnectionLimit)
	assert.Equal(t, float32(50), ac.NetClientUserErrors)
	assert.Equal(t, float32(50), ac.PrepareUserErrors)
}

func TestKeyspacesAuditor_MissingDataAndOnDemandAndAllFaults(t *testing.T) {
	ctx := timeseries.NewContext(1000, 1300, 60)
	ones := timeseries.NewWithData(ctx.From, ctx.Step, []float32{1, 1, 1, 1, 1, 1})
	p := &db.Project{Id: "p1", Name: "default"}

	// Case A: Discovered Keyspaces with no CloudWatch data -> UNKNOWN ("no data")
	wNoData := model.NewWorld(ctx.From, ctx.To, ctx.Step, ctx.Step)
	appNoData := wNoData.GetOrCreateApplication(model.NewApplicationId("p1", "", model.ApplicationKindKeyspaces, "acc-reg"), false)
	instNoData := appNoData.GetOrCreateInstance("acc-reg", nil)
	instNoData.Keyspaces = model.NewKeyspaces("acc/reg")
	instNoData.Keyspaces.LifeSpan = ones
	Audit(wNoData, p, appNoData, nil)

	var ksRep *model.AuditReport
	var instRep *model.AuditReport
	for _, r := range appNoData.Reports {
		if r.Name == model.AuditReportKeyspaces {
			ksRep = r
		}
		if r.Name == model.AuditReportInstances {
			instRep = r
		}
	}
	require.NotNil(t, ksRep)
	assert.Equal(t, model.UNKNOWN, ksRep.Status)
	require.NotNil(t, instRep)
	assert.Equal(t, model.OK, instRep.Status, "container-less Keyspaces instance must report OK in Instances audit")

	// Case B: On-demand only table -> Capacity check says "not checked for on-demand tables" (OK)
	wOD := model.NewWorld(ctx.From, ctx.To, ctx.Step, ctx.Step)
	appOD := wOD.GetOrCreateApplication(model.NewApplicationId("p1", "", model.ApplicationKindKeyspaces, "acc-reg"), false)
	instOD := appOD.GetOrCreateInstance("acc-reg", nil)
	instOD.Keyspaces = model.NewKeyspaces("acc/reg")
	instOD.Keyspaces.LifeSpan = ones
	tOD := instOD.Keyspaces.GetOrCreateTable("ks", "t_clean")
	tOD.CapacityMode.Update(ones, "PAY_PER_REQUEST")
	tOD.Status.Update(ones, "ACTIVE")
	tOD.ConsumedRead = timeseries.NewWithData(ctx.From, ctx.Step, []float32{2, 2, 2, 2, 2, timeseries.NaN})
	Audit(wOD, p, appOD, nil)
	ch, _ := appOD.GetCheckWithReport(model.Checks.KeyspacesCapacity.Id)
	require.NotNil(t, ch)
	assert.Equal(t, model.OK, ch.Status)
	assert.Contains(t, ch.Message, "not checked for on-demand tables")

	// Case C: All faults active (with trailing NaN to prove windowed evaluation resists CW lag!)
	wFault := model.NewWorld(ctx.From, ctx.To, ctx.Step, ctx.Step)
	appFault := wFault.GetOrCreateApplication(model.NewApplicationId("p1", "", model.ApplicationKindKeyspaces, "acc-reg"), false)
	instFault := appFault.GetOrCreateInstance("acc-reg", nil)
	ks := model.NewKeyspaces("acc/reg")
	ks.LifeSpan = ones
	instFault.Keyspaces = ks

	tProv := ks.GetOrCreateTable("ks", "t_prov")
	tProv.CapacityMode.Update(ones, "PROVISIONED")
	tProv.Status.Update(ones, "ACTIVE")
	tProv.ProvisionedWrite = timeseries.NewWithData(ctx.From, ctx.Step, []float32{5, 5, 5, 5, 5, 5})
	tProv.ConsumedWrite = timeseries.NewWithData(ctx.From, ctx.Step, []float32{4.6, 4.6, 4.6, 4.6, 4.6, timeseries.NaN})
	tProv.WriteThrottleEvents = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 596, 596, 596, timeseries.NaN})
	tProv.UserErrors = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 596, 596, 596, timeseries.NaN})

	tHot := ks.GetOrCreateTable("ks", "t_ondemand")
	tHot.CapacityMode.Update(ones, "PAY_PER_REQUEST")
	tHot.Status.Update(ones, "ACTIVE")
	tHot.PartitionLimitEvents = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 48698, 48698, timeseries.NaN, timeseries.NaN})
	tHot.WriteThrottleEvents = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 48698, 48698, timeseries.NaN, timeseries.NaN})
	tHot.UserErrors = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 48698, 48698, timeseries.NaN, timeseries.NaN})

	ks.PerConnectionLimitEvents = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 144839, 144839, timeseries.NaN, timeseries.NaN})
	ks.AccountUserErrors["EXECUTE"] = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 144839, 144839, timeseries.NaN, timeseries.NaN})
	ks.AccountUserErrors["PREPARE"] = timeseries.NewWithData(ctx.From, ctx.Step, []float32{0, 0, 50, 50, timeseries.NaN, timeseries.NaN})

	Audit(wFault, p, appFault, nil)

	chHot, _ := appFault.GetCheckWithReport(model.Checks.KeyspacesHotPartition.Id)
	assert.Equal(t, model.WARNING, chHot.Status)
	assert.Contains(t, chHot.Message, "ks.t_ondemand")
	assert.Contains(t, chHot.Message, "partition key")

	chThr, _ := appFault.GetCheckWithReport(model.Checks.KeyspacesThrottling.Id)
	assert.Equal(t, model.WARNING, chThr.Status)
	assert.Contains(t, chThr.Message, "ks.t_prov")
	assert.NotContains(t, chThr.Message, "ks.t_ondemand", "hot partition must be subtracted from capacity throttling")

	chCap, _ := appFault.GetCheckWithReport(model.Checks.KeyspacesCapacity.Id)
	assert.Equal(t, model.WARNING, chCap.Status)
	assert.Contains(t, chCap.Message, "ks.t_prov")

	chPC, _ := appFault.GetCheckWithReport(model.Checks.KeyspacesPerConnectionLimit.Id)
	assert.Equal(t, model.WARNING, chPC.Status)
	assert.Contains(t, chPC.Message, "raise connections per host")

	chUE, _ := appFault.GetCheckWithReport(model.Checks.KeyspacesUserErrors.Id)
	assert.Equal(t, model.WARNING, chUE.Status)
	assert.Contains(t, chUE.Message, "PREPARE invalid queries (50)")
	assert.NotContains(t, chUE.Message, "ks.t_prov", "throttles must be subtracted from table UserErrors")

	// Verify clientAvgLatency divides total latency rate by request count rate (not raw total latency)
	conn := &model.AppToAppConnection{
		RequestsCount: map[model.Protocol]map[string]*timeseries.TimeSeries{
			model.ProtocolCassandra: {"ok": timeseries.NewWithData(ctx.From, ctx.Step, []float32{100, 100, 100, 100, 100, 100})},
		},
		RequestsLatency: map[model.Protocol]*timeseries.TimeSeries{
			model.ProtocolCassandra: timeseries.NewWithData(ctx.From, ctx.Step, []float32{20, 20, 20, 20, 20, 20}),
		},
	}
	avgLat := clientAvgLatency(conn, model.ProtocolCassandra)
	require.NotNil(t, avgLat)
	assert.InDelta(t, 0.2, float64(avgLat.Last()), 0.001)
}
