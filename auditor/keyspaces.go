package auditor

import (
	"slices"
	"strings"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

// CloudWatch publishes a metric 1.5-3 minutes late, so a shorter window lets an active fault flicker back to OK.
const keyspacesCheckWindow = 10 * timeseries.Minute

type keyspacesTableRejections struct {
	hotPartition *timeseries.TimeSeries
	throttling   *timeseries.TimeSeries
	invalid      *timeseries.TimeSeries
	system       *timeseries.TimeSeries
}

type keyspacesAccountRejections struct {
	connectionLimit *timeseries.TimeSeries
	invalidExecute  *timeseries.TimeSeries
	invalidPrepare  *timeseries.TimeSeries
}

func (a *appAuditor) keyspaces() {
	instance := keyspacesInstance(a.app)
	if instance == nil {
		return
	}
	k := instance.Keyspaces

	report := a.addReport(model.AuditReportKeyspaces)
	hotPartitionCheck := report.CreateCheck(model.Checks.KeyspacesHotPartition)
	throttlingCheck := report.CreateCheck(model.Checks.KeyspacesThrottling)
	capacityCheck := report.CreateCheck(model.Checks.KeyspacesCapacity)
	connectionLimitCheck := report.CreateCheck(model.Checks.KeyspacesConnectionLimit)
	systemErrorsCheck := report.CreateCheck(model.Checks.KeyspacesSystemErrors)
	userErrorsCheck := report.CreateCheck(model.Checks.KeyspacesUserErrors)
	checks := []*model.Check{hotPartitionCheck, throttlingCheck, capacityCheck, connectionLimitCheck, systemErrorsCheck, userErrorsCheck}

	table := report.GetOrCreateTable("Table", "Capacity mode", "Consumed capacity", "Size", "Issues")
	tableErrorsChart := report.GetOrCreateChartGroup("Rejected requests to <selector>, per second", nil)
	capacityChart := report.GetOrCreateChartGroup("Consumed capacity of <selector>, units per second", nil)
	accountErrorsChart := report.GetOrCreateChart("Rejected requests without a table, per second", nil)
	for _, ch := range checks {
		ch.AddWidget(table.Widget())
	}
	for _, ch := range []*model.Check{hotPartitionCheck, throttlingCheck, systemErrorsCheck, userErrorsCheck} {
		ch.AddWidget(tableErrorsChart.Widget())
	}
	capacityCheck.AddWidget(capacityChart.Widget())
	connectionLimitCheck.AddWidget(accountErrorsChart.Widget())
	userErrorsCheck.AddWidget(accountErrorsChart.Widget())

	from := a.w.Ctx.To.Add(-keyspacesCheckWindow)
	exceeds := func(ch *model.Check, ts *timeseries.TimeSeries, item string) bool {
		if peak := ts.ReduceFrom(from, timeseries.Max); timeseries.IsNaN(peak) || peak <= ch.Threshold {
			return false
		}
		ch.AddItem("%s", item)
		return true
	}

	hasData := len(k.AccountErrors) > 0
	provisioned := false
	for _, key := range sortedKeyspacesTables(k) {
		t := k.Tables[key]
		name := key.String()
		r := tableRejections(t)
		readUsage := capacityUsage(t.ConsumedRead, t.ProvisionedRead)
		writeUsage := capacityUsage(t.ConsumedWrite, t.ProvisionedWrite)
		hasData = hasData || t.HasData()
		provisioned = provisioned || t.IsProvisioned()

		var issues []string
		if exceeds(hotPartitionCheck, r.hotPartition, name) {
			issues = append(issues, "hot partition: spread the traffic across more partition keys (a partition serves up to 1,000 WCU and 3,000 RCU)")
		}
		if exceeds(throttlingCheck, r.throttling, name) {
			issues = append(issues, "throttling: "+keyspacesCapacityAdvice(t))
		}
		if t.IsProvisioned() && (exceeds(capacityCheck, readUsage, name) || exceeds(capacityCheck, writeUsage, name)) {
			issues = append(issues, "capacity: "+keyspacesCapacityAdvice(t))
		}
		if exceeds(systemErrorsCheck, r.system, name) {
			issues = append(issues, "system errors: the error is on the AWS side, check the AWS Health Dashboard")
		}
		if exceeds(userErrorsCheck, r.invalid, name) {
			issues = append(issues, "invalid requests: fix the queries, the schema or the column names")
		}

		table.AddRow(keyspacesTableRow(name, t, readUsage, writeUsage, issues)...)
		addKeyspacesTableCharts(report, name, t, r)
	}

	ar := accountRejections(k)
	exceeds(connectionLimitCheck, ar.connectionLimit, instance.Name)
	exceeds(userErrorsCheck, ar.invalidExecute, "EXECUTE statements")
	exceeds(userErrorsCheck, ar.invalidPrepare, "PREPARE statements")
	if len(k.AccountErrors) > 0 {
		accountErrorsChart.Stacked().
			AddSeries("per-connection limit", perSecond(ar.connectionLimit)).
			AddSeries("invalid EXECUTE", perSecond(ar.invalidExecute)).
			AddSeries("invalid PREPARE", perSecond(ar.invalidPrepare))
	}

	if !provisioned {
		capacityCheck.SetStatus(model.OK, "not checked for on-demand tables")
	}
	if !hasData {
		for _, ch := range checks {
			ch.SetStatus(model.UNKNOWN, "no data")
		}
	}
}

func keyspacesInstance(app *model.Application) *model.Instance {
	for _, i := range app.Instances {
		if i.Keyspaces != nil {
			return i
		}
	}
	return nil
}

func sortedKeyspacesTables(k *model.Keyspaces) []model.KeyspacesTableKey {
	keys := make([]model.KeyspacesTableKey, 0, len(k.Tables))
	for key := range k.Tables {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b model.KeyspacesTableKey) int { return strings.Compare(a.String(), b.String()) })
	return keys
}

// AWS counts one rejection in several metrics: a hot partition also increments the write throttle events,
// and a throttled request also increments the table's UserErrors. Subtracting them leaves each rejection in one check.
func tableRejections(t *model.KeyspacesTable) keyspacesTableRejections {
	throttles := timeseries.NewAggregate(timeseries.NanSum).
		Add(t.Errors[model.KeyspacesErrorReadThrottle], t.Errors[model.KeyspacesErrorWriteThrottle]).Get()
	return keyspacesTableRejections{
		hotPartition: t.Errors[model.KeyspacesErrorHotPartition],
		throttling:   subtract(throttles, t.Errors[model.KeyspacesErrorHotPartition]),
		invalid:      subtract(t.Errors[model.KeyspacesErrorUser], throttles),
		system:       t.Errors[model.KeyspacesErrorSystem],
	}
}

// A per-connection limit rejection also increments the account's UserErrors of EXECUTE.
func accountRejections(k *model.Keyspaces) keyspacesAccountRejections {
	connectionLimit := timeseries.NewAggregate(timeseries.NanSum)
	for key, ts := range k.AccountErrors {
		if key.Type == model.KeyspacesErrorPerConnectionLimit {
			connectionLimit.Add(ts)
		}
	}
	accountErrors := func(typ, operation string) *timeseries.TimeSeries {
		return k.AccountErrors[model.KeyspacesAccountErrorKey{Type: typ, Operation: operation}]
	}
	return keyspacesAccountRejections{
		connectionLimit: connectionLimit.Get(),
		invalidExecute: subtract(
			accountErrors(model.KeyspacesErrorUser, model.KeyspacesOperationExecute),
			accountErrors(model.KeyspacesErrorPerConnectionLimit, model.KeyspacesOperationExecute),
		),
		invalidPrepare: accountErrors(model.KeyspacesErrorUser, model.KeyspacesOperationPrepare),
	}
}

func keyspacesCapacityAdvice(t *model.KeyspacesTable) string {
	switch {
	case !t.IsProvisioned():
		return "the traffic exceeded the warm throughput or the previous peak of the on-demand table, pre-warm the table"
	case t.AutoScaling.Value() == "true":
		return "auto scaling doesn't keep up, raise its minimum or maximum capacity"
	}
	return "raise the provisioned capacity or enable auto scaling"
}

func keyspacesTableRow(name string, t *model.KeyspacesTable, readUsage, writeUsage *timeseries.TimeSeries, issues []string) []*model.TableCell {
	mode := "on-demand"
	if t.IsProvisioned() {
		mode = "provisioned"
		if t.AutoScaling.Value() == "true" {
			mode += ", auto scaling"
		}
	}

	var usage []string
	if v := readUsage.Last(); !timeseries.IsNaN(v) {
		usage = append(usage, "read "+utils.FormatPercentage(v))
	}
	if v := writeUsage.Last(); !timeseries.IsNaN(v) {
		usage = append(usage, "write "+utils.FormatPercentage(v))
	}

	size := model.NewTableCell()
	if v := t.SizeBytes.Last(); !timeseries.IsNaN(v) {
		value, unit := utils.FormatBytes(v)
		size.SetValue(value).SetUnit(unit)
	}

	issuesCell := model.NewTableCell(issues...)
	switch {
	case len(issues) > 0:
		issuesCell.UpdateStatus(model.WARNING)
	case !t.HasData():
		issuesCell.SetStatus(model.UNKNOWN, "no data")
	}

	return []*model.TableCell{model.NewTableCell(name), model.NewTableCell(mode), model.NewTableCell(strings.Join(usage, ", ")), size, issuesCell}
}

func addKeyspacesTableCharts(report *model.AuditReport, name string, t *model.KeyspacesTable, r keyspacesTableRejections) {
	capacity := report.GetOrCreateChartInGroup("Consumed capacity of <selector>, units per second", name, nil).
		AddSeries("read", t.ConsumedRead).
		AddSeries("write", t.ConsumedWrite)
	if t.IsProvisioned() {
		capacity.AddSeries("provisioned read", t.ProvisionedRead, "grey").AddSeries("provisioned write", t.ProvisionedWrite, "black")
	}

	if len(t.Latency) > 0 {
		latency := map[string]model.SeriesData{}
		for op, ts := range t.Latency {
			latency[op] = ts
		}
		report.GetOrCreateChartInGroup("Latency of <selector>, p99, seconds", name, nil).AddMany(latency, 5, timeseries.Max)
	}

	if len(t.Requests) > 0 {
		requests := map[string]model.SeriesData{}
		for op, ts := range t.Requests {
			requests[op] = perSecond(ts)
		}
		report.GetOrCreateChartInGroup("Successful requests to <selector>, per second", name, nil).Stacked().AddMany(requests, 5, timeseries.NanSum)
	}

	if len(t.Errors) > 0 {
		report.GetOrCreateChartInGroup("Rejected requests to <selector>, per second", name, nil).Stacked().
			AddSeries("hot partition", perSecond(r.hotPartition)).
			AddSeries("throttling", perSecond(r.throttling)).
			AddSeries("invalid requests", perSecond(r.invalid)).
			AddSeries("system errors", perSecond(r.system))
	}
}

func capacityUsage(consumed, provisioned *timeseries.TimeSeries) *timeseries.TimeSeries {
	return timeseries.Aggregate2(consumed, provisioned, func(c, p float32) float32 {
		if p <= 0 {
			return timeseries.NaN
		}
		return c / p * 100
	})
}

// AWS increments the overlapping counters almost one-to-one: under a fault of millions of rejections they differ
// by a few events, so a residual within this share of the subtracted counter is not a separate rejection.
const keyspacesOverlapTolerance = 0.01

// subtract returns max(0, x-y) at each point, a missing y counts as zero.
func subtract(x, y *timeseries.TimeSeries) *timeseries.TimeSeries {
	if x.IsEmpty() || y.IsEmpty() {
		return x
	}
	return timeseries.Aggregate2(x, y, func(x, y float32) float32 {
		if timeseries.IsNaN(y) {
			return x
		}
		if x-y <= y*keyspacesOverlapTolerance {
			return 0
		}
		return x - y
	})
}

func perSecond(windowCount *timeseries.TimeSeries) *timeseries.TimeSeries {
	return windowCount.Map(func(t timeseries.Time, v float32) float32 { return v / float32(model.KeyspacesErrorsWindow) })
}
