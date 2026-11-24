package constructor

import (
	"sort"
	"strconv"
	"strings"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

func (c *Constructor) loadSLIs(w *model.World, metrics map[string][]*model.MetricValues, project *db.Project) {
	inboundAvailabilityAgg := inboundAvailability(w, metrics[qRecordingRuleApplicationL7InboundRequests+"_avg"], project)
	inboundAvailabilityRaw := inboundAvailability(w, metrics[qRecordingRuleApplicationL7InboundRequests+"_raw"], project)
	inboundLatencyAgg := inboundLatency(w, metrics[qRecordingRuleApplicationL7InboundHistogram+"_avg"], project)
	inboundLatencyRaw := inboundLatency(w, metrics[qRecordingRuleApplicationL7InboundHistogram+"_raw"], project)
	builtinAvailabilityAgg := builtinAvailability(w, metrics[qRecordingRuleApplicationL7Requests+"_avg"], project)
	builtinAvailabilityRaw := builtinAvailability(w, metrics[qRecordingRuleApplicationL7Requests+"_raw"], project)
	builtinLatencyAgg := builtinLatency(w, metrics[qRecordingRuleApplicationL7Histogram+"_avg"], project)
	builtinLatencyRaw := builtinLatency(w, metrics[qRecordingRuleApplicationL7Histogram+"_raw"], project)
	llmLatencyAgg := inboundLatency(w, metrics[qRecordingRuleApplicationLLMInboundHistogram+"_avg"], project)
	llmLatencyRaw := inboundLatency(w, metrics[qRecordingRuleApplicationLLMInboundHistogram+"_raw"], project)
	keyspacesAvailabilityAgg := keyspacesAvailability(w, metrics, "_avg")
	keyspacesAvailabilityRaw := keyspacesAvailability(w, metrics, "_raw")

	customAvailabilityAgg := map[model.ApplicationId]availabilitySlis{}
	customAvailabilityRaw := map[model.ApplicationId]availabilitySlis{}
	customLatencyAgg := map[model.ApplicationId][]model.HistogramBucket{}
	customLatencyRaw := map[model.ApplicationId][]model.HistogramBucket{}
	loadCustomSLIs(metrics, customAvailabilityAgg, customLatencyAgg, project, "_avg")
	loadCustomSLIs(metrics, customAvailabilityRaw, customLatencyRaw, project, "_raw")

	for _, app := range w.Applications {
		availabilityCfg, _ := w.CheckConfigs.GetAvailability(app.Id)
		if availabilityCfg.Custom {
			agg := customAvailabilityAgg[app.Id]
			raw := customAvailabilityRaw[app.Id]
			if agg.failed.IsEmpty() {
				agg.failed = agg.total.WithNewValue(timeseries.NaN)
			}
			app.AvailabilitySLIs = append(app.AvailabilitySLIs, &model.AvailabilitySLI{
				Config:        availabilityCfg,
				TotalRequests: agg.total, TotalRequestsRaw: raw.total,
				FailedRequests: agg.failed, FailedRequestsRaw: raw.failed,
			})
		} else {
			agg, ok := inboundAvailabilityAgg[app.Id]
			raw := inboundAvailabilityRaw[app.Id]
			if !ok || agg.total.IsEmpty() {
				agg = builtinAvailabilityAgg[app.Id]
				raw = builtinAvailabilityRaw[app.Id]
			}
			if agg.total.IsEmpty() {
				if ks, ok := keyspacesAvailabilityAgg[app.Id]; ok {
					agg = ks
					raw = keyspacesAvailabilityRaw[app.Id]
				}
			}
			if agg.failed.IsEmpty() {
				agg.failed = agg.total.WithNewValue(timeseries.NaN)
			}
			if !agg.total.IsEmpty() {
				app.AvailabilitySLIs = append(app.AvailabilitySLIs, &model.AvailabilitySLI{
					Config:        availabilityCfg,
					TotalRequests: agg.total, TotalRequestsRaw: raw.total,
					FailedRequests: agg.failed, FailedRequestsRaw: raw.failed,
				})
			}
		}

		latencyCfg, _ := w.CheckConfigs.GetLatency(app.Id, app.Category)
		if latencyCfg.Custom {
			app.LatencySLIs = append(app.LatencySLIs, &model.LatencySLI{
				Config:    latencyCfg,
				Histogram: customLatencyAgg[app.Id], HistogramRaw: customLatencyRaw[app.Id],
			})
		} else {
			agg := inboundLatencyAgg[app.Id]
			raw := inboundLatencyRaw[app.Id]
			if len(agg) == 0 {
				agg = builtinLatencyAgg[app.Id]
				raw = builtinLatencyRaw[app.Id]
			}
			if len(agg) > 0 {
				app.LatencySLIs = append(app.LatencySLIs, &model.LatencySLI{
					Config:    latencyCfg,
					Histogram: agg, HistogramRaw: raw,
				})
			}
		}
		if agg := llmLatencyAgg[app.Id]; len(agg) > 0 {
			cfg, _ := w.CheckConfigs.GetLLMLatency(app.Id)
			app.LLMLatencySLI = &model.LatencySLI{Config: cfg, Histogram: agg, HistogramRaw: llmLatencyRaw[app.Id]}
		}
	}
}

func inboundAvailability(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId]availabilitySlis {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		if w.GetApplication(appId) == nil {
			continue
		}
		status := mv.Labels["status"]
		if byApp[appId] == nil {
			byApp[appId] = map[string]*timeseries.TimeSeries{}
		}
		byApp[appId][status] = merge(byApp[appId][status], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId]availabilitySlis{}
	for appId, byStatus := range byApp {
		total := timeseries.NewAggregate(timeseries.NanSum)
		failed := timeseries.NewAggregate(timeseries.NanSum)
		for status, ts := range byStatus {
			total.Add(ts)
			if model.IsRequestStatusFailed(status) {
				failed.Add(ts)
			}
		}
		res[appId] = availabilitySlis{total: total.Get(), failed: failed.Get()}
	}
	return res
}

func inboundLatency(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId][]model.HistogramBucket {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		if w.GetApplication(appId) == nil {
			continue
		}
		buckets := byApp[appId]
		if buckets == nil {
			buckets = map[string]*timeseries.TimeSeries{}
			byApp[appId] = buckets
		}
		leStr := mv.Labels["le"]
		buckets[leStr] = merge(buckets[leStr], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId][]model.HistogramBucket{}
	for appId, buckets := range byApp {
		res[appId] = histogramBuckets(buckets)
	}
	return res
}

func loadCustomSLIs(metrics map[string][]*model.MetricValues,
	availabilityRaw map[model.ApplicationId]availabilitySlis,
	latencyRaw map[model.ApplicationId][]model.HistogramBucket,
	project *db.Project,
	suffix string,
) {
	for queryName, values := range metrics {
		if len(values) == 0 || !strings.HasPrefix(queryName, qApplicationCustomSLI) {
			continue
		}
		parts := strings.Split(queryName, "/")
		if len(parts) != 3 {
			continue
		}
		appId, _ := model.NewApplicationIdFromString(parts[1], project.ClusterId())
		switch parts[2] {
		case "total_requests" + suffix:
			a := availabilityRaw[appId]
			a.total = values[0].Values
			availabilityRaw[appId] = a
		case "failed_requests" + suffix:
			a := availabilityRaw[appId]
			a.failed = values[0].Values
			availabilityRaw[appId] = a
		case "requests_histogram" + suffix:
			buckets := map[string]*timeseries.TimeSeries{}
			for _, mv := range values {
				leStr := mv.Labels["le"]
				buckets[leStr] = mv.Values
			}
			latencyRaw[appId] = histogramBuckets(buckets)
		}
	}
}

var keyspacesSLIQueries = []string{"aws_keyspaces_table_requests", "aws_keyspaces_table_errors", "aws_keyspaces_account_errors"}

// clients that eBPF cannot see in plaintext (Java async SSLEngine, Node.js TLS) leave a Keyspaces application without
// request metrics, so its availability falls back to CloudWatch. AWS counts one rejection in several metrics:
// a table rejection is the larger of its UserErrors and its throttle events, and an account-level EXECUTE rejection is
// the larger of the account UserErrors and the per-connection limit events. PREPARE errors are client mistakes.
func keyspacesAvailability(w *model.World, metrics map[string][]*model.MetricValues, suffix string) map[model.ApplicationId]availabilitySlis {
	appsByKeyspacesId := map[string]model.ApplicationId{}
	for _, app := range w.Applications {
		for _, i := range app.Instances {
			if k := i.Keyspaces; k != nil {
				appsByKeyspacesId[k.Id] = app.Id
			}
		}
	}
	if len(appsByKeyspacesId) == 0 {
		return nil
	}
	type tableKey struct {
		id, keyspace, table string
	}
	type perTable struct {
		user, throttles *timeseries.Aggregate
	}
	type perAccount struct {
		good, system, perConnection, execute *timeseries.Aggregate
	}
	tables := map[tableKey]*perTable{}
	accounts := map[string]*perAccount{}
	account := func(id string) *perAccount {
		a := accounts[id]
		if a == nil {
			a = &perAccount{
				good:          timeseries.NewAggregate(timeseries.NanSum),
				system:        timeseries.NewAggregate(timeseries.NanSum),
				perConnection: timeseries.NewAggregate(timeseries.NanSum),
				execute:       timeseries.NewAggregate(timeseries.NanSum),
			}
			accounts[id] = a
		}
		return a
	}
	table := func(m *model.MetricValues) *perTable {
		key := tableKey{id: m.Labels["keyspaces_id"], keyspace: m.Labels["keyspace"], table: m.Labels["table"]}
		account(key.id)
		t := tables[key]
		if t == nil {
			t = &perTable{user: timeseries.NewAggregate(timeseries.NanSum), throttles: timeseries.NewAggregate(timeseries.NanSum)}
			tables[key] = t
		}
		return t
	}
	for _, m := range metrics["aws_keyspaces_table_requests"+suffix] {
		account(m.Labels["keyspaces_id"]).good.Add(m.Values)
	}
	for _, m := range metrics["aws_keyspaces_table_errors"+suffix] {
		switch m.Labels["type"] {
		case model.KeyspacesErrorUser:
			table(m).user.Add(m.Values)
		case model.KeyspacesErrorReadThrottle, model.KeyspacesErrorWriteThrottle:
			table(m).throttles.Add(m.Values)
		case model.KeyspacesErrorSystem:
			account(m.Labels["keyspaces_id"]).system.Add(m.Values)
		}
	}
	for _, m := range metrics["aws_keyspaces_account_errors"+suffix] {
		a := account(m.Labels["keyspaces_id"])
		switch {
		case m.Labels["type"] == model.KeyspacesErrorPerConnectionLimit:
			a.perConnection.Add(m.Values)
		case m.Labels["type"] == model.KeyspacesErrorUser && m.Labels["operation"] == model.KeyspacesOperationExecute:
			a.execute.Add(m.Values)
		}
	}

	failedByAccount := map[string]*timeseries.Aggregate{}
	for key, t := range tables {
		if failedByAccount[key.id] == nil {
			failedByAccount[key.id] = timeseries.NewAggregate(timeseries.NanSum)
		}
		failedByAccount[key.id].Add(timeseries.NewAggregate(timeseries.Max).Add(t.user.Get(), t.throttles.Get()).Get())
	}
	perSecond := func(ts *timeseries.TimeSeries) *timeseries.TimeSeries {
		return ts.Map(func(t timeseries.Time, v float32) float32 { return v / float32(model.KeyspacesErrorsWindow) })
	}
	res := map[model.ApplicationId]availabilitySlis{}
	for id, a := range accounts {
		appId, ok := appsByKeyspacesId[id]
		if !ok {
			continue
		}
		failed := timeseries.NewAggregate(timeseries.NanSum).Add(
			timeseries.NewAggregate(timeseries.Max).Add(a.execute.Get(), a.perConnection.Get()).Get(),
			a.system.Get(),
		)
		if f := failedByAccount[id]; f != nil {
			failed.Add(f.Get())
		}
		total := timeseries.NewAggregate(timeseries.NanSum).Add(a.good.Get(), failed.Get())
		res[appId] = availabilitySlis{total: perSecond(total.Get()), failed: perSecond(failed.Get())}
	}
	return res
}

type availabilitySlis struct {
	total  *timeseries.TimeSeries
	failed *timeseries.TimeSeries
}

func builtinAvailability(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId]availabilitySlis {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		dstId, err := model.NewApplicationIdFromString(mv.Labels["dest"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		srcId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		status := mv.Labels["status"]
		src := w.GetApplication(srcId)
		dst := w.GetApplication(dstId)
		if src == nil || dst == nil {
			continue
		}
		if !dst.Category.Auxiliary() && src.Category.Auxiliary() {
			continue
		}
		if byApp[dstId] == nil {
			byApp[dstId] = map[string]*timeseries.TimeSeries{}
		}
		byApp[dstId][status] = merge(byApp[dstId][status], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId]availabilitySlis{}
	for appId, byStatus := range byApp {
		total := timeseries.NewAggregate(timeseries.NanSum)
		failed := timeseries.NewAggregate(timeseries.NanSum)
		for status, ts := range byStatus {
			total.Add(ts)
			if model.IsRequestStatusFailed(status) {
				failed.Add(ts)
			}
		}
		res[appId] = availabilitySlis{total: total.Get(), failed: failed.Get()}
	}
	return res
}

func builtinLatency(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId][]model.HistogramBucket {
	if len(values) == 0 {
		return nil
	}

	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		app := w.GetApplication(appId)
		if app == nil {
			continue
		}
		buckets := byApp[appId]
		if buckets == nil {
			buckets = map[string]*timeseries.TimeSeries{}
			byApp[appId] = buckets
		}
		leStr := mv.Labels["le"]
		buckets[leStr] = merge(buckets[leStr], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId][]model.HistogramBucket{}
	for appId, buckets := range byApp {
		res[appId] = histogramBuckets(buckets)
	}
	return res
}

func histogramBuckets(values map[string]*timeseries.TimeSeries) []model.HistogramBucket {
	buckets := make([]model.HistogramBucket, 0, len(values))
	for leStr, ts := range values {
		le, err := strconv.ParseFloat(leStr, 64)
		if err != nil {
			klog.Warningln(err)
			continue
		}
		buckets = append(buckets, model.HistogramBucket{Le: float32(le), TimeSeries: ts})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Le < buckets[j].Le
	})
	return buckets
}
