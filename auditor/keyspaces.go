package auditor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

const keyspacesEvalWindow = 10 * timeseries.Minute

type keyspacesTableClassification struct {
	HotPartition        float32
	ReadCapThrottle     float32
	WriteCapThrottle    float32
	NetClientUserErrors float32
	SystemErrors        float32
}

type keyspacesAccountClassification struct {
	PerConnectionLimit  float32
	NetClientUserErrors float32
	PrepareUserErrors   float32
}

func nanToZero(v float32) float32 {
	if timeseries.IsNaN(v) || v < 0 {
		return 0
	}
	return v
}

// classifyKeyspacesTableRejections subtracts overlapping CloudWatch counters (verified 1:1 in spike):
//   - HotPartition = StoragePartitionThroughputCapacityExceeded
//   - CapacityThrottle = max(0, ThrottleEvents - HotPartition)
//   - NetClientUserErrors = max(0, TableUserErrors - TotalThrottleEvents)
func classifyKeyspacesTableRejections(readThrottle, writeThrottle, hotPartition, tableUserErrors, systemErrors float32) keyspacesTableClassification {
	rt := nanToZero(readThrottle)
	wt := nanToZero(writeThrottle)
	hp := nanToZero(hotPartition)
	ue := nanToZero(tableUserErrors)
	se := nanToZero(systemErrors)

	// Hot-partition events triple-count into WriteThrottleEvents (or ReadThrottleEvents if read-heavy).
	writeCap := wt - hp
	readCap := rt
	if writeCap < 0 {
		rem := -writeCap
		writeCap = 0
		readCap -= rem
		if readCap < 0 {
			readCap = 0
		}
	}

	totalThrottle := rt + wt
	if hp > totalThrottle {
		totalThrottle = hp
	}
	netUserErr := ue - totalThrottle
	if netUserErr < 0 {
		netUserErr = 0
	}

	return keyspacesTableClassification{
		HotPartition:        hp,
		ReadCapThrottle:     readCap,
		WriteCapThrottle:    writeCap,
		NetClientUserErrors: netUserErr,
		SystemErrors:        se,
	}
}

// classifyKeyspacesAccountRejections subtracts PerConnectionRequestRateExceeded from account-level
// UserErrors[Operation=EXECUTE] and adds account-level UserErrors[Operation=PREPARE].
func classifyKeyspacesAccountRejections(perConn, execUserErrors, prepareUserErrors float32) keyspacesAccountClassification {
	pc := nanToZero(perConn)
	ex := nanToZero(execUserErrors)
	pr := nanToZero(prepareUserErrors)

	netExec := ex - pc
	if netExec < 0 {
		netExec = 0
	}
	return keyspacesAccountClassification{
		PerConnectionLimit:  pc,
		NetClientUserErrors: netExec + pr,
		PrepareUserErrors:   pr,
	}
}

// trailingWindowMax evaluates the maximum non-NaN value across the trailing evaluation window,
// avoiding single-point Last() reads that can falsely appear OK during CloudWatch ingestion lag.
func trailingWindowMax(ts *timeseries.TimeSeries, ctx timeseries.Context, window timeseries.Duration) float32 {
	if ts.IsEmpty() {
		return timeseries.NaN
	}
	cutoff := ctx.To.Add(-window)
	var maxVal float32 = timeseries.NaN
	iter := ts.Iter()
	for iter.Next() {
		t, v := iter.Value()
		if t.Before(cutoff) || timeseries.IsNaN(v) {
			continue
		}
		if timeseries.IsNaN(maxVal) || v > maxVal {
			maxVal = v
		}
	}
	return maxVal
}

func subNonNegative(a, b *timeseries.TimeSeries) *timeseries.TimeSeries {
	if a.IsEmpty() {
		return nil
	}
	if b.IsEmpty() {
		return a
	}
	return timeseries.Sub(a, b).Map(func(t timeseries.Time, v float32) float32 {
		if timeseries.IsNaN(v) || v < 0 {
			return 0
		}
		return v
	})
}

func (a *appAuditor) keyspaces() {
	if !a.app.IsKeyspaces() {
		return
	}

	report := a.addReport(model.AuditReportKeyspaces)

	hotPartCheck := report.CreateCheck(model.Checks.KeyspacesHotPartition)
	throttleCheck := report.CreateCheck(model.Checks.KeyspacesThrottling)
	capacityCheck := report.CreateCheck(model.Checks.KeyspacesCapacity)
	perConnCheck := report.CreateCheck(model.Checks.KeyspacesPerConnectionLimit)
	latencyGapCheck := report.CreateCheck(model.Checks.KeyspacesLatencyGap)
	systemErrCheck := report.CreateCheck(model.Checks.KeyspacesSystemErrors)
	userErrCheck := report.CreateCheck(model.Checks.KeyspacesUserErrors)

	var ks *model.Keyspaces
	for _, i := range a.app.Instances {
		if i.Keyspaces != nil {
			ks = i.Keyspaces
			break
		}
	}
	if ks == nil {
		report.Status = model.UNKNOWN
		return
	}

	tableWidget := report.GetOrCreateTable("Table", "Mode", "Status", "Provisioned / Warm", "Consumed RCU / WCU", "p99 Latency", "Rejections / Errors", "Size")
	capacityCharts := report.GetOrCreateChartGroup("Consumed vs. Provisioned Capacity on <selector>, units/s", nil)
	latencyChart := report.GetOrCreateChart("Keyspaces server-side p99 latency, seconds", nil)
	rejectionsChart := report.GetOrCreateChart("Keyspaces rejections and errors (classified, 5m window)", nil).Stacked()

	hotPartCheck.AddWidget(rejectionsChart.Widget())
	throttleCheck.AddWidget(rejectionsChart.Widget())
	capacityCheck.AddWidget(capacityCharts.Widget())
	perConnCheck.AddWidget(rejectionsChart.Widget())
	latencyGapCheck.AddWidget(latencyChart.Widget())
	systemErrCheck.AddWidget(rejectionsChart.Widget())
	userErrCheck.AddWidget(rejectionsChart.Widget())

	if !ks.HasCloudWatchData() {
		for _, ch := range []*model.Check{hotPartCheck, throttleCheck, capacityCheck, perConnCheck, latencyGapCheck, systemErrCheck, userErrCheck} {
			ch.SetStatus(model.UNKNOWN, "no data")
		}
		report.Status = model.UNKNOWN
		return
	}

	// Deterministic table ordering
	tableKeys := make([]model.KeyspacesTableKey, 0, len(ks.Tables))
	for tk := range ks.Tables {
		tableKeys = append(tableKeys, tk)
	}
	sort.Slice(tableKeys, func(i, j int) bool {
		return tableKeys[i].String() < tableKeys[j].String()
	})

	var (
		hotPartTables     []string
		throttleMsgs      []string
		capacityMsgs      []string
		systemErrTables   []string
		userErrMsgs       []string
		provisionedCount  int
		maxServerP99      float32
		totalHotPartTs    = timeseries.NewAggregate(timeseries.NanSum)
		totalCapThrTs     = timeseries.NewAggregate(timeseries.NanSum)
		totalNetUserErrTs = timeseries.NewAggregate(timeseries.NanSum)
		totalSysErrTs     = timeseries.NewAggregate(timeseries.NanSum)
	)

	for _, tk := range tableKeys {
		t := ks.Tables[tk]
		name := tk.String()

		readThr := trailingWindowMax(t.ReadThrottleEvents, a.w.Ctx, keyspacesEvalWindow)
		writeThr := trailingWindowMax(t.WriteThrottleEvents, a.w.Ctx, keyspacesEvalWindow)
		hotPart := trailingWindowMax(t.PartitionLimitEvents, a.w.Ctx, keyspacesEvalWindow)
		tableUE := trailingWindowMax(t.UserErrors, a.w.Ctx, keyspacesEvalWindow)
		sysErr := trailingWindowMax(t.SystemErrors, a.w.Ctx, keyspacesEvalWindow)

		cls := classifyKeyspacesTableRejections(readThr, writeThr, hotPart, tableUE, sysErr)

		// Track aggregate classified breakdown series for the Rejections chart
		if !t.PartitionLimitEvents.IsEmpty() {
			totalHotPartTs.Add(t.PartitionLimitEvents)
		}
		tableTotalThrTs := timeseries.NewAggregate(timeseries.NanSum).Add(t.ReadThrottleEvents, t.WriteThrottleEvents).Get()
		if capThrTs := subNonNegative(tableTotalThrTs, t.PartitionLimitEvents); !capThrTs.IsEmpty() {
			totalCapThrTs.Add(capThrTs)
		}
		if netUETs := subNonNegative(t.UserErrors, tableTotalThrTs); !netUETs.IsEmpty() {
			totalNetUserErrTs.Add(netUETs)
		}
		if !t.SystemErrors.IsEmpty() {
			totalSysErrTs.Add(t.SystemErrors)
		}

		consR := trailingWindowMax(t.ConsumedRead, a.w.Ctx, keyspacesEvalWindow)
		consW := trailingWindowMax(t.ConsumedWrite, a.w.Ctx, keyspacesEvalWindow)
		provR := t.ProvisionedRead.Last()
		provW := t.ProvisionedWrite.Last()
		warmR := t.WarmRead.Last()
		warmW := t.WarmWrite.Last()

		if cls.HotPartition > hotPartCheck.Threshold {
			hotPartTables = append(hotPartTables, fmt.Sprintf("%s (%.0f)", name, cls.HotPartition))
		}

		if cls.ReadCapThrottle > throttleCheck.Threshold || cls.WriteCapThrottle > throttleCheck.Threshold {
			var dirs []string
			if cls.ReadCapThrottle > 0 {
				dirs = append(dirs, fmt.Sprintf("read %.0f", cls.ReadCapThrottle))
			}
			if cls.WriteCapThrottle > 0 {
				dirs = append(dirs, fmt.Sprintf("write %.0f", cls.WriteCapThrottle))
			}
			if t.IsProvisioned() {
				pct := maxCapPercent(consR, provR, consW, provW)
				throttleMsgs = append(throttleMsgs, fmt.Sprintf("%s (%s) at %.0f%% of provisioned capacity — raise RCU/WCU or enable auto scaling", name, strings.Join(dirs, ", "), pct))
			} else {
				throttleMsgs = append(throttleMsgs, fmt.Sprintf("%s (%s) — traffic exceeded table's warm throughput / previous peak", name, strings.Join(dirs, ", ")))
			}
		}

		if t.IsProvisioned() {
			provisionedCount++
			if !timeseries.IsNaN(consR) && !timeseries.IsNaN(provR) && provR > 0 {
				pctR := consR / provR * 100
				if pctR > capacityCheck.Threshold {
					capacityMsgs = append(capacityMsgs, fmt.Sprintf("%s read at %.0f%% (%.1f/%.0f RCU)", name, pctR, consR, provR))
				}
			}
			if !timeseries.IsNaN(consW) && !timeseries.IsNaN(provW) && provW > 0 {
				pctW := consW / provW * 100
				if pctW > capacityCheck.Threshold {
					capacityMsgs = append(capacityMsgs, fmt.Sprintf("%s write at %.0f%% (%.1f/%.0f WCU)", name, pctW, consW, provW))
				}
			}
		}

		if cls.SystemErrors > systemErrCheck.Threshold {
			systemErrTables = append(systemErrTables, fmt.Sprintf("%s (%.0f)", name, cls.SystemErrors))
		}
		if cls.NetClientUserErrors > userErrCheck.Threshold {
			userErrMsgs = append(userErrMsgs, fmt.Sprintf("%s (%.0f)", name, cls.NetClientUserErrors))
		}

		// Track server p99 latency across table operations
		var tableMaxP99 float32 = timeseries.NaN
		ops := make([]string, 0, len(t.LatencyP99))
		for op := range t.LatencyP99 {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		for _, op := range ops {
			ts := t.LatencyP99[op]
			if latencyChart != nil && !ts.IsEmpty() {
				latencyChart.AddSeries(name+" "+op, ts)
			}
			v := trailingWindowMax(ts, a.w.Ctx, keyspacesEvalWindow)
			if !timeseries.IsNaN(v) {
				if timeseries.IsNaN(tableMaxP99) || v > tableMaxP99 {
					tableMaxP99 = v
				}
				if v > maxServerP99 {
					maxServerP99 = v
				}
			}
		}

		// Per-table Capacity chart
		if capacityCharts != nil {
			ch := capacityCharts.GetOrCreateChart(name)
			if !t.ConsumedRead.IsEmpty() {
				ch.AddSeries("consumed read", t.ConsumedRead)
			}
			if !t.ConsumedWrite.IsEmpty() {
				ch.AddSeries("consumed write", t.ConsumedWrite)
			}
			if t.IsProvisioned() {
				if !t.ProvisionedRead.IsEmpty() {
					ch.AddSeries("provisioned read", t.ProvisionedRead)
				}
				if !t.ProvisionedWrite.IsEmpty() {
					ch.AddSeries("provisioned write", t.ProvisionedWrite)
				}
			}
		}

		// Summary table row
		if tableWidget != nil {
			modeCell := model.NewTableCell(t.CapacityMode.Value())
			statusCell := model.NewTableCell().SetStatus(model.OK, t.Status.Value())
			if t.Status.Value() != "" && t.Status.Value() != "ACTIVE" {
				statusCell.SetStatus(model.WARNING, t.Status.Value())
			}

			limitStr := "—"
			if t.IsProvisioned() && !timeseries.IsNaN(provR) && !timeseries.IsNaN(provW) {
				limitStr = fmt.Sprintf("%.0f R / %.0f W (prov)", provR, provW)
			} else if !timeseries.IsNaN(warmR) && !timeseries.IsNaN(warmW) {
				limitStr = fmt.Sprintf("%.0f R / %.0f W (warm)", warmR, warmW)
			}

			consStr := fmt.Sprintf("%s R / %s W", formatOptFloat(consR), formatOptFloat(consW))

			latStr := "—"
			if !timeseries.IsNaN(tableMaxP99) {
				latStr = utils.FormatLatency(tableMaxP99)
			}

			var errParts []string
			if cls.HotPartition > 0 {
				errParts = append(errParts, fmt.Sprintf("hot-partition:%.0f", cls.HotPartition))
			}
			if cls.ReadCapThrottle > 0 {
				errParts = append(errParts, fmt.Sprintf("read-thr:%.0f", cls.ReadCapThrottle))
			}
			if cls.WriteCapThrottle > 0 {
				errParts = append(errParts, fmt.Sprintf("write-thr:%.0f", cls.WriteCapThrottle))
			}
			if cls.NetClientUserErrors > 0 {
				errParts = append(errParts, fmt.Sprintf("user-err:%.0f", cls.NetClientUserErrors))
			}
			if cls.SystemErrors > 0 {
				errParts = append(errParts, fmt.Sprintf("sys-err:%.0f", cls.SystemErrors))
			}
			errCell := model.NewTableCell("—")
			if len(errParts) > 0 {
				errCell.SetStatus(model.WARNING, strings.Join(errParts, ", "))
			}

			sizeStr := "—"
			if sz := t.TableSizeBytes.Last(); !timeseries.IsNaN(sz) {
				val, unit := utils.FormatBytes(sz)
				sizeStr = val + " " + unit
			}

			tableWidget.AddRow(
				model.NewTableCell(name),
				modeCell,
				statusCell,
				model.NewTableCell(limitStr),
				model.NewTableCell(consStr),
				model.NewTableCell(latStr),
				errCell,
				model.NewTableCell(sizeStr),
			)
		}
	}

	// Account-level rejections: PerConnectionRequestRateExceeded and Account UserErrors (EXECUTE / PREPARE)
	perConnVal := trailingWindowMax(ks.PerConnectionLimitEvents, a.w.Ctx, keyspacesEvalWindow)
	execUEVal := trailingWindowMax(ks.AccountUserErrors["EXECUTE"], a.w.Ctx, keyspacesEvalWindow)
	prepUEVal := trailingWindowMax(ks.AccountUserErrors["PREPARE"], a.w.Ctx, keyspacesEvalWindow)
	acctCls := classifyKeyspacesAccountRejections(perConnVal, execUEVal, prepUEVal)

	if acctCls.NetClientUserErrors > userErrCheck.Threshold {
		if acctCls.PrepareUserErrors > 0 {
			userErrMsgs = append(userErrMsgs, fmt.Sprintf("PREPARE invalid queries (%.0f)", acctCls.PrepareUserErrors))
		}
		if netExec := acctCls.NetClientUserErrors - acctCls.PrepareUserErrors; netExec > 0 {
			userErrMsgs = append(userErrMsgs, fmt.Sprintf("EXECUTE client errors (%.0f)", netExec))
		}
	}

	if netExecTs := subNonNegative(ks.AccountUserErrors["EXECUTE"], ks.PerConnectionLimitEvents); !netExecTs.IsEmpty() {
		totalNetUserErrTs.Add(netExecTs)
	}
	if !ks.AccountUserErrors["PREPARE"].IsEmpty() {
		totalNetUserErrTs.Add(ks.AccountUserErrors["PREPARE"])
	}

	if rejectionsChart != nil {
		if ts := totalHotPartTs.Get(); !ts.IsEmpty() {
			rejectionsChart.AddSeries("hot partition", ts)
		}
		if ts := totalCapThrTs.Get(); !ts.IsEmpty() {
			rejectionsChart.AddSeries("capacity throttle", ts)
		}
		if !ks.PerConnectionLimitEvents.IsEmpty() {
			rejectionsChart.AddSeries("per-connection limit", ks.PerConnectionLimitEvents)
		}
		if ts := totalNetUserErrTs.Get(); !ts.IsEmpty() {
			rejectionsChart.AddSeries("client user errors", ts)
		}
		if ts := totalSysErrTs.Get(); !ts.IsEmpty() {
			rejectionsChart.AddSeries("system errors", ts)
		}
	}

	// Finalize checks with actionable remediation advice
	if len(hotPartTables) > 0 {
		hotPartCheck.SetStatus(model.WARNING, "%s: partition key exceeds single-partition throughput (~1,000 WCU / 3,000 RCU); spread traffic across more partition keys.", strings.Join(hotPartTables, ", "))
	}

	if len(throttleMsgs) > 0 {
		throttleCheck.SetStatus(model.WARNING, "%s.", strings.Join(throttleMsgs, "; "))
	}

	if provisionedCount == 0 {
		capacityCheck.SetStatus(model.OK, "not checked for on-demand tables")
	} else if len(capacityMsgs) > 0 {
		capacityCheck.SetStatus(model.WARNING, "%s — approaching provisioned limit; raise RCU/WCU or enable auto scaling.", strings.Join(capacityMsgs, "; "))
	}

	if acctCls.PerConnectionLimit > perConnCheck.Threshold {
		clients := describeKeyspacesClients(a.app)
		if len(clients) > 0 {
			perConnCheck.SetStatus(model.WARNING, "%.0f per-connection limit rejections; %s — raise connections per host in the driver.", acctCls.PerConnectionLimit, strings.Join(clients, "; "))
		} else {
			perConnCheck.SetStatus(model.WARNING, "%.0f per-connection limit rejections (~3,000 req/s per TCP connection) — raise connections per host in the driver.", acctCls.PerConnectionLimit)
		}
	}

	// Client vs. server latency gap check
	var gapClients []string
	if maxServerP99 > 0 {
		for _, d := range a.app.Downstreams {
			if d.Application == nil || d.Application == a.app {
				continue
			}
			clientLat := clientAvgLatency(d, model.ProtocolCassandra)
			if clientLat.IsEmpty() {
				continue
			}
			cl := trailingWindowMax(clientLat, a.w.Ctx, keyspacesEvalWindow)
			if !timeseries.IsNaN(cl) && cl-maxServerP99 > latencyGapCheck.Threshold {
				var hints []string
				if rtt := trailingWindowMax(d.Rtt, a.w.Ctx, keyspacesEvalWindow); !timeseries.IsNaN(rtt) && rtt > 0.02 {
					hints = append(hints, fmt.Sprintf("RTT %s", utils.FormatLatency(rtt)))
				}
				if retr := trailingWindowMax(d.Retransmissions, a.w.Ctx, keyspacesEvalWindow); !timeseries.IsNaN(retr) && retr > 0 {
					hints = append(hints, "TCP retransmits")
				}
				if failed := trailingWindowMax(d.FailedConnections, a.w.Ctx, keyspacesEvalWindow); !timeseries.IsNaN(failed) && failed > 0 {
					hints = append(hints, "failed connects")
				}
				if d.Application.IsJvm() {
					hints = append(hints, "check JVM GC")
				}
				if d.Application.IsNodejs() {
					hints = append(hints, "check Node.js event loop")
				}
				hintStr := "check network RTT, TCP retransmits, or client runtime stalls"
				if len(hints) > 0 {
					hintStr = strings.Join(hints, ", ")
				}
				gapClients = append(gapClients, fmt.Sprintf("%s client %s vs. server p99 %s (%s)", d.Application.Id.Name, utils.FormatLatency(cl), utils.FormatLatency(maxServerP99), hintStr))
			}
		}
	}
	if len(gapClients) > 0 {
		sort.Strings(gapClients)
		latencyGapCheck.SetStatus(model.WARNING, "%s.", strings.Join(gapClients, "; "))
	}

	if len(systemErrTables) > 0 {
		systemErrCheck.SetStatus(model.WARNING, "%s: errors on the AWS side; check AWS Health.", strings.Join(systemErrTables, ", "))
	}

	if len(userErrMsgs) > 0 {
		userErrCheck.SetStatus(model.WARNING, "%s — application query, schema, or invalid-column errors (not capacity throttling).", strings.Join(userErrMsgs, "; "))
	}
}

func maxCapPercent(consR, provR, consW, provW float32) float32 {
	var best float32
	if !timeseries.IsNaN(consR) && !timeseries.IsNaN(provR) && provR > 0 {
		if p := consR / provR * 100; p > best {
			best = p
		}
	}
	if !timeseries.IsNaN(consW) && !timeseries.IsNaN(provW) && provW > 0 {
		if p := consW / provW * 100; p > best {
			best = p
		}
	}
	return best
}

func formatOptFloat(v float32) string {
	if timeseries.IsNaN(v) {
		return "—"
	}
	return fmt.Sprintf("%.1f", v)
}

func clientAvgLatency(d *model.AppToAppConnection, proto model.Protocol) *timeseries.TimeSeries {
	totalLat := d.RequestsLatency[proto]
	byStatus := d.RequestsCount[proto]
	if totalLat.IsEmpty() || len(byStatus) == 0 {
		return nil
	}
	totalReq := timeseries.NewAggregate(timeseries.NanSum)
	for _, ts := range byStatus {
		totalReq.Add(ts)
	}
	reqTs := totalReq.Get()
	if reqTs.IsEmpty() {
		return nil
	}
	return timeseries.Div(totalLat, reqTs)
}

func describeKeyspacesClients(app *model.Application) []string {
	var out []string
	for _, d := range app.Downstreams {
		if d.Application == nil || d.Application == app {
			continue
		}
		var rps float32
		if byStatus := d.RequestsCount[model.ProtocolCassandra]; byStatus != nil {
			agg := timeseries.NewAggregate(timeseries.NanSum)
			for _, ts := range byStatus {
				agg.Add(ts)
			}
			if v := agg.Get().Reduce(timeseries.Max); !timeseries.IsNaN(v) {
				rps = v
			}
		}
		conns := d.Active.Last()
		if timeseries.IsNaN(conns) {
			conns = d.Active.Reduce(timeseries.Max)
		}
		switch {
		case rps > 0 && !timeseries.IsNaN(conns) && conns > 0:
			out = append(out, fmt.Sprintf("%s sends ~%.0f req/s over %.0f connections", d.Application.Id.Name, rps, conns))
		case !timeseries.IsNaN(conns) && conns > 0:
			out = append(out, fmt.Sprintf("%s holds %.0f active connections", d.Application.Id.Name, conns))
		default:
			out = append(out, d.Application.Id.Name)
		}
	}
	sort.Strings(out)
	return out
}
