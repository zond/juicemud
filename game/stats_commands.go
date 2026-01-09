package game

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/rodaine/table"
	"github.com/zond/juicemud/storage"
)

// statsContext holds the context for stats subcommand execution.
type statsContext struct {
	c     *Connection
	stats *JSStats
	parts []string
}

// statsHandler is a function that handles a stats subcommand.
type statsHandler func(ctx *statsContext) error

// statsSubcommand defines a stats subcommand with its handler and help text.
type statsSubcommand struct {
	handler statsHandler
	help    string
}

// statsSubcommands maps subcommand names to their handlers.
var statsSubcommands = map[string]statsSubcommand{
	"summary":   {handler: handleStatsSummary, help: "Dashboard view (default)"},
	"dashboard": {handler: handleStatsSummary, help: "Dashboard view (alias for summary)"},
	"errors":    {handler: handleStatsErrors, help: "Error stats (sub: summary|categories|locations|recent)"},
	"perf":      {handler: handleStatsPerf, help: "Performance stats (sub: summary|scripts|slow)"},
	"scripts":   {handler: handleStatsScripts, help: "Top n scripts (sort: time|execs|slow|errors|errorrate)"},
	"script":    {handler: handleStatsScript, help: "Detailed stats for specific script"},
	"objects":   {handler: handleStatsObjects, help: "Top n objects (sort: time|execs|slow|errors|errorrate)"},
	"object":    {handler: handleStatsObject, help: "Detailed stats for specific object"},
	"intervals": {handler: handleStatsIntervals, help: "Top n intervals (sort: time|execs|slow|errors|errorrate)"},
	"users":     {handler: handleStatsUsers, help: "List users (filter: all|owners|wizards|players; sort: name|id|login|stale)"},
	"flush":     {handler: handleStatsFlush, help: "Show database flush health status"},
	"reset":     {handler: handleStatsReset, help: "Clear all statistics"},
}

// parseIntArg parses an integer argument from parts at the given index, returning defaultVal if not present or invalid.
func parseIntArg(parts []string, index int, defaultVal int) int {
	if len(parts) > index {
		if parsed, err := strconv.Atoi(parts[index]); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultVal
}

// parseScriptSortArg parses a script sort argument.
func parseScriptSortArg(parts []string, index int, sortMap map[string]ScriptSortField, defaultSort ScriptSortField) ScriptSortField {
	if len(parts) > index {
		if sort, ok := sortMap[parts[index]]; ok {
			return sort
		}
	}
	return defaultSort
}

// parseObjectSortArg parses an object sort argument.
func parseObjectSortArg(parts []string, index int, sortMap map[string]ObjectSortField, defaultSort ObjectSortField) ObjectSortField {
	if len(parts) > index {
		if sort, ok := sortMap[parts[index]]; ok {
			return sort
		}
	}
	return defaultSort
}

// parseIntervalSortArg parses an interval sort argument.
func parseIntervalSortArg(parts []string, index int, sortMap map[string]IntervalSortField, defaultSort IntervalSortField) IntervalSortField {
	if len(parts) > index {
		if sort, ok := sortMap[parts[index]]; ok {
			return sort
		}
	}
	return defaultSort
}

// parseUserSortArg parses a user sort argument.
func parseUserSortArg(parts []string, index int, sortMap map[string]storage.UserSortField, defaultSort storage.UserSortField) storage.UserSortField {
	if len(parts) > index {
		if sort, ok := sortMap[parts[index]]; ok {
			return sort
		}
	}
	return defaultSort
}

// printStatsHelp prints the main stats help message.
func printStatsHelp(w io.Writer) {
	fmt.Fprintln(w, "usage: /stats [subcommand]")
	fmt.Fprintln(w, "  summary                    Dashboard view (default)")
	fmt.Fprintln(w, "  errors [sub]               Error stats (sub: summary|categories|locations|recent)")
	fmt.Fprintln(w, "  perf [sub]                 Performance stats (sub: summary|scripts|slow)")
	fmt.Fprintln(w, "  scripts [sort] [n]         Top n scripts (sort: time|execs|slow|errors|errorrate)")
	fmt.Fprintln(w, "  script <path>              Detailed stats for specific script")
	fmt.Fprintln(w, "  objects [sort] [n]         Top n objects (sort: time|execs|slow|errors|errorrate)")
	fmt.Fprintln(w, "  object <id>                Detailed stats for specific object")
	fmt.Fprintln(w, "  intervals [sort] [n]       Top n intervals (sort: time|execs|slow|errors|errorrate)")
	fmt.Fprintln(w, "  users [filter] [sort] [n]  List users (filter: all|owners|wizards|players; sort: name|id|login|stale)")
	fmt.Fprintln(w, "  flush                      Show database flush health status")
	fmt.Fprintln(w, "  reset                      Clear all statistics")
}

// handleStatsSummary displays the dashboard summary view.
func handleStatsSummary(ctx *statsContext) error {
	c := ctx.c
	stats := ctx.stats
	g := stats.GlobalSnapshot()

	fmt.Fprintf(c.term, "Server Status (uptime: %s)\n\n", g.Uptime.Round(time.Second))

	fmt.Fprintln(c.term, "EXECUTIONS")
	fmt.Fprintf(c.term, "  Total: %d    Rate: %.2f/s (1m: %.1f/s, 1h: %.0f/s)\n",
		g.TotalExecs, g.ExecRates.PerSecond, g.ExecRates.PerMinute, g.ExecRates.PerHour)
	fmt.Fprintf(c.term, "  Avg: %.1fms    Slow: %d (%.1f%%)\n",
		g.AvgTimeMs, g.TotalSlow, g.SlowPercent)
	fmt.Fprintf(c.term, "  JS CPU: %.1f%%\n", g.TimeRates.PerSecond*100)

	fmt.Fprintln(c.term, "\nERRORS")
	fmt.Fprintf(c.term, "  Total: %d (%.2f%%)    Rate: %.2f/s (1m: %.2f/s, 1h: %.1f/s)\n",
		g.TotalErrors, g.ErrorPercent, g.ErrorRates.PerSecond, g.ErrorRates.PerMinute, g.ErrorRates.PerHour)
	if len(g.ByCategory) > 0 {
		fmt.Fprint(c.term, "  ")
		first := true
		for cat, count := range g.ByCategory {
			if !first {
				fmt.Fprint(c.term, ", ")
			}
			fmt.Fprintf(c.term, "%s: %d", cat, count)
			first = false
		}
		fmt.Fprintln(c.term)
	}

	// Show top issues
	topLocs := stats.TopLocations(3)
	slowRecs := stats.RecentSlowExecutions(3)
	if len(topLocs) > 0 || len(slowRecs) > 0 {
		fmt.Fprintln(c.term, "\nTOP ISSUES")
		if len(topLocs) > 0 {
			fmt.Fprintln(c.term, "  Errors:")
			for _, loc := range topLocs {
				fmt.Fprintf(c.term, "    %s  %d\n", truncateForDisplay(loc.Location, 30), loc.Count)
			}
		}
		if len(slowRecs) > 0 {
			fmt.Fprintln(c.term, "  Slow:")
			for _, rec := range slowRecs {
				fmt.Fprintf(c.term, "    %s  %.0fms\n", truncateForDisplay(rec.SourcePath, 25), float64(rec.Duration.Milliseconds()))
			}
		}
	}

	// User statistics
	fmt.Fprintln(c.term, "\nUSERS")
	totalUsers, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterAll)
	wizardCount, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterWizards)
	ownerCount, _ := c.game.storage.CountUsers(c.ctx, storage.UserFilterOwners)
	onlineCount := c.game.connectionByObjectID.Len()
	fmt.Fprintf(c.term, "  Total: %d    Wizards: %d    Owners: %d\n", totalUsers, wizardCount, ownerCount)
	if recentUser, err := c.game.storage.GetMostRecentLogin(c.ctx); err == nil && recentUser != nil {
		ago := time.Since(recentUser.LastLogin()).Truncate(time.Second)
		fmt.Fprintf(c.term, "  Online: %d    Last login: %v ago (%s)\n", onlineCount, ago, recentUser.Name)
	} else {
		fmt.Fprintf(c.term, "  Online: %d\n", onlineCount)
	}

	// Storage health
	fmt.Fprintln(c.term, "\nSTORAGE")
	health := c.game.storage.FlushHealth()
	if health.Healthy() {
		flushAgo := "never"
		if !health.LastFlushAt.IsZero() {
			flushAgo = time.Since(health.LastFlushAt).Truncate(time.Second).String() + " ago"
		}
		fmt.Fprintf(c.term, "  Flush: OK (%s)\n", flushAgo)
	} else {
		fmt.Fprint(c.term, "  Flush: FAILING")
		if health.LastError != nil {
			fmt.Fprintf(c.term, " - %v", health.LastError)
		}
		fmt.Fprintln(c.term)
	}

	return nil
}

// Error subcommand handlers

type errorsSubHandler func(ctx *statsContext, parts []string) error

var errorsSubcommands = map[string]errorsSubHandler{
	"summary":    handleErrorsSummary,
	"categories": handleErrorsCategories,
	"locations":  handleErrorsLocations,
	"recent":     handleErrorsRecent,
}

func handleStatsErrors(ctx *statsContext) error {
	subcmd := "summary"
	if len(ctx.parts) >= 3 {
		subcmd = ctx.parts[2]
	}

	if handler, ok := errorsSubcommands[subcmd]; ok {
		return handler(ctx, ctx.parts)
	}

	fmt.Fprintln(ctx.c.term, "usage: /stats errors [subcommand]")
	fmt.Fprintln(ctx.c.term, "  summary              Show error summary (default)")
	fmt.Fprintln(ctx.c.term, "  categories           Show errors by category")
	fmt.Fprintln(ctx.c.term, "  locations [n]        Show top n error locations (default 20)")
	fmt.Fprintln(ctx.c.term, "  recent [n]           Show n most recent errors (default 10)")
	return nil
}

func handleErrorsSummary(ctx *statsContext, _ []string) error {
	c := ctx.c
	stats := ctx.stats
	g := stats.GlobalSnapshot()

	fmt.Fprintf(c.term, "Error Summary (total: %d, rate: %.2f%%)\n\n", g.TotalErrors, g.ErrorPercent)
	if len(g.ByCategory) > 0 {
		fmt.Fprintln(c.term, "By category:")
		for cat, count := range g.ByCategory {
			fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
		}
	}
	fmt.Fprintf(c.term, "\nError rates: %.2f/s, %.1f/m, %.0f/h\n",
		g.ErrorRates.PerSecond, g.ErrorRates.PerMinute, g.ErrorRates.PerHour)

	// Show recent errors
	recent := stats.RecentErrors(5)
	if len(recent) > 0 {
		fmt.Fprintln(c.term, "\nRecent errors:")
		for _, rec := range recent {
			locStr := rec.Location.String()
			fmt.Fprintf(c.term, "  [%s] %s %s: %s\n",
				rec.Timestamp.Format("15:04:05"),
				rec.Category,
				locStr,
				rec.Message)
		}
	}
	return nil
}

func handleErrorsCategories(ctx *statsContext, _ []string) error {
	g := ctx.stats.GlobalSnapshot()
	if len(g.ByCategory) == 0 {
		fmt.Fprintln(ctx.c.term, "No errors recorded.")
		return nil
	}
	t := table.New("Category", "Count").WithWriter(ctx.c.term)
	for cat, count := range g.ByCategory {
		t.AddRow(string(cat), count)
	}
	t.Print()
	return nil
}

func handleErrorsLocations(ctx *statsContext, parts []string) error {
	n := parseIntArg(parts, 3, 20)
	locs := ctx.stats.TopLocations(n)
	if len(locs) == 0 {
		fmt.Fprintln(ctx.c.term, "No error locations recorded.")
		return nil
	}
	t := table.New("Location", "Count", "Last Seen").WithWriter(ctx.c.term)
	for _, loc := range locs {
		t.AddRow(loc.Location, loc.Count, loc.LastSeen.Format(time.RFC3339))
	}
	t.Print()
	return nil
}

func handleErrorsRecent(ctx *statsContext, parts []string) error {
	n := parseIntArg(parts, 3, 10)
	recent := ctx.stats.RecentErrors(n)
	if len(recent) == 0 {
		fmt.Fprintln(ctx.c.term, "No recent errors.")
		return nil
	}
	for _, rec := range recent {
		locStr := rec.Location.String()
		fmt.Fprintf(ctx.c.term, "[%s] %s %s @ %s: %s\n",
			rec.Timestamp.Format("15:04:05"),
			rec.ObjectID,
			rec.Category,
			locStr,
			rec.Message)
	}
	return nil
}

// Perf subcommand handlers

type perfSubHandler func(ctx *statsContext, parts []string) error

var perfSubcommands = map[string]perfSubHandler{
	"summary": handlePerfSummary,
	"scripts": handlePerfScripts,
	"slow":    handlePerfSlow,
}

func handleStatsPerf(ctx *statsContext) error {
	subcmd := "summary"
	if len(ctx.parts) >= 3 {
		subcmd = ctx.parts[2]
	}

	if handler, ok := perfSubcommands[subcmd]; ok {
		return handler(ctx, ctx.parts)
	}

	fmt.Fprintln(ctx.c.term, "usage: /stats perf [subcommand]")
	fmt.Fprintln(ctx.c.term, "  summary              Show performance summary (default)")
	fmt.Fprintln(ctx.c.term, "  scripts [sort] [n]   Show top n scripts (sort: time|execs|slow)")
	fmt.Fprintln(ctx.c.term, "  slow [n]             Show n most recent slow executions (default 10)")
	return nil
}

func handlePerfSummary(ctx *statsContext, _ []string) error {
	c := ctx.c
	g := ctx.stats.GlobalSnapshot()

	fmt.Fprintf(c.term, "Performance Summary\n\n")
	fmt.Fprintf(c.term, "Total executions: %d\n", g.TotalExecs)
	fmt.Fprintf(c.term, "Total JS time: %.1fs\n", g.TotalTimeMs/1000)
	fmt.Fprintf(c.term, "Average time: %.1fms\n", g.AvgTimeMs)
	fmt.Fprintf(c.term, "Slow executions: %d (%.2f%%)\n", g.TotalSlow, g.SlowPercent)
	fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
		g.ExecRates.PerSecond, g.ExecRates.PerMinute, g.ExecRates.PerHour)
	fmt.Fprintf(c.term, "Time rates (JS seconds per wall time):\n")
	fmt.Fprintf(c.term, "  Current: %.3fs/s (%.1f%% CPU)\n", g.TimeRates.PerSecond, g.TimeRates.PerSecond*100)
	fmt.Fprintf(c.term, "  Per minute: %.1fs/m\n", g.TimeRates.PerMinute)
	fmt.Fprintf(c.term, "  Per hour: %.0fs/h\n", g.TimeRates.PerHour)
	return nil
}

var scriptSortMap = map[string]ScriptSortField{
	"time":  SortScriptByTime,
	"execs": SortScriptByExecs,
	"slow":  SortScriptBySlow,
}

var scriptSortMapFull = map[string]ScriptSortField{
	"time":      SortScriptByTime,
	"execs":     SortScriptByExecs,
	"slow":      SortScriptBySlow,
	"errors":    SortScriptByErrors,
	"errorrate": SortScriptByErrorRate,
}

func handlePerfScripts(ctx *statsContext, parts []string) error {
	sortBy := parseScriptSortArg(parts, 3, scriptSortMap, SortScriptByTime)
	n := parseIntArg(parts, 4, 20)

	scripts := ctx.stats.TopScripts(sortBy, n)
	if len(scripts) == 0 {
		fmt.Fprintln(ctx.c.term, "No scripts recorded.")
		return nil
	}

	t := table.New("Source Path", "Execs", "Avg(ms)", "Max(ms)", "Slow%").WithWriter(ctx.c.term)
	for _, script := range scripts {
		t.AddRow(
			script.SourcePath,
			script.Executions,
			fmt.Sprintf("%.1f", script.AvgTimeMs),
			fmt.Sprintf("%.1f", script.MaxTimeMs),
			fmt.Sprintf("%.1f", script.SlowPercent),
		)
	}
	t.Print()
	return nil
}

func handlePerfSlow(ctx *statsContext, parts []string) error {
	n := parseIntArg(parts, 3, 10)
	recent := ctx.stats.RecentSlowExecutions(n)
	if len(recent) == 0 {
		fmt.Fprintln(ctx.c.term, "No slow executions recorded.")
		return nil
	}
	for _, rec := range recent {
		fmt.Fprintf(ctx.c.term, "[%s] #%s %s %.1fms\n",
			rec.Timestamp.Format("15:04:05"),
			rec.ObjectID,
			rec.SourcePath,
			float64(rec.Duration.Milliseconds()))
		if len(rec.ImportChain) > 1 {
			fmt.Fprintf(ctx.c.term, "  Imports: ")
			for i, dep := range rec.ImportChain[1:] { // Skip first (the source itself)
				if i > 0 {
					fmt.Fprint(ctx.c.term, ", ")
				}
				fmt.Fprint(ctx.c.term, dep)
			}
			fmt.Fprintln(ctx.c.term)
		}
	}
	return nil
}

// Scripts subcommand handler

func handleStatsScripts(ctx *statsContext) error {
	sortBy := parseScriptSortArg(ctx.parts, 2, scriptSortMapFull, SortScriptByTime)
	n := parseIntArg(ctx.parts, 3, 20)

	scripts := ctx.stats.TopScripts(sortBy, n)
	if len(scripts) == 0 {
		fmt.Fprintln(ctx.c.term, "No scripts recorded.")
		return nil
	}

	t := table.New("Source Path", "Execs", "Avg(ms)", "Max(ms)", "Slow%", "Errs", "Err%").WithWriter(ctx.c.term)
	for _, script := range scripts {
		t.AddRow(
			script.SourcePath,
			script.Executions,
			fmt.Sprintf("%.1f", script.AvgTimeMs),
			fmt.Sprintf("%.1f", script.MaxTimeMs),
			fmt.Sprintf("%.1f", script.SlowPercent),
			script.Errors,
			fmt.Sprintf("%.1f", script.ErrorPercent),
		)
	}
	t.Print()
	return nil
}

// Script subcommand handler

func handleStatsScript(ctx *statsContext) error {
	c := ctx.c
	if len(ctx.parts) < 3 {
		fmt.Fprintln(c.term, "usage: /stats script <path>")
		return nil
	}
	sourcePath := ctx.parts[2]
	script := ctx.stats.ScriptSnapshot(sourcePath)
	if script == nil {
		fmt.Fprintf(c.term, "No stats for script %q\n", sourcePath)
		return nil
	}

	fmt.Fprintf(c.term, "Script: %s\n", script.SourcePath)
	fmt.Fprintf(c.term, "Executions: %d\n", script.Executions)
	fmt.Fprintf(c.term, "Time: avg=%.1fms, min=%.1fms, max=%.1fms\n",
		script.AvgTimeMs, script.MinTimeMs, script.MaxTimeMs)
	fmt.Fprintf(c.term, "Slow: %d (%.2f%%)\n", script.SlowCount, script.SlowPercent)
	fmt.Fprintf(c.term, "Errors: %d (%.2f%%)\n", script.Errors, script.ErrorPercent)
	if !script.LastExecution.IsZero() {
		fmt.Fprintf(c.term, "Last execution: %s\n", script.LastExecution.Format(time.RFC3339))
	}
	if !script.LastError.IsZero() {
		fmt.Fprintf(c.term, "Last error: %s\n", script.LastError.Format(time.RFC3339))
	}
	fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
		script.ExecRates.PerSecond, script.ExecRates.PerMinute, script.ExecRates.PerHour)
	fmt.Fprintf(c.term, "Time rates: %.3fs/s, %.1fs/m, %.0fs/h\n",
		script.TimeRates.PerSecond, script.TimeRates.PerMinute, script.TimeRates.PerHour)
	fmt.Fprintf(c.term, "Error rates: %.2f/s, %.1f/m, %.0f/h\n",
		script.ErrorRates.PerSecond, script.ErrorRates.PerMinute, script.ErrorRates.PerHour)
	if len(script.ByCategory) > 0 {
		fmt.Fprintln(c.term, "\nErrors by category:")
		for cat, count := range script.ByCategory {
			fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
		}
	}
	if len(script.ByLocation) > 0 {
		fmt.Fprintln(c.term, "\nErrors by location:")
		for loc, count := range script.ByLocation {
			fmt.Fprintf(c.term, "  %s: %d\n", loc, count)
		}
	}
	if len(script.ImportChain) > 1 {
		fmt.Fprintln(c.term, "\nImport chain:")
		for _, dep := range script.ImportChain {
			fmt.Fprintf(c.term, "  %s\n", dep)
		}
	}
	return nil
}

// Objects subcommand handler

var objectSortMap = map[string]ObjectSortField{
	"time":      SortObjectByTime,
	"execs":     SortObjectByExecs,
	"slow":      SortObjectBySlow,
	"errors":    SortObjectByErrors,
	"errorrate": SortObjectByErrorRate,
}

func handleStatsObjects(ctx *statsContext) error {
	sortBy := parseObjectSortArg(ctx.parts, 2, objectSortMap, SortObjectByTime)
	n := parseIntArg(ctx.parts, 3, 20)

	objs := ctx.stats.TopObjects(sortBy, n)
	if len(objs) == 0 {
		fmt.Fprintln(ctx.c.term, "No objects recorded.")
		return nil
	}

	t := table.New("Object ID", "Source", "Execs", "Avg(ms)", "Slow%", "Errs", "Err%").WithWriter(ctx.c.term)
	for _, obj := range objs {
		t.AddRow(
			obj.ObjectID,
			truncateForDisplay(obj.SourcePath, 20),
			obj.Executions,
			fmt.Sprintf("%.1f", obj.AvgTimeMs),
			fmt.Sprintf("%.1f", obj.SlowPercent),
			obj.Errors,
			fmt.Sprintf("%.1f", obj.ErrorPercent),
		)
	}
	t.Print()
	return nil
}

// Object subcommand handler

func handleStatsObject(ctx *statsContext) error {
	c := ctx.c
	if len(ctx.parts) < 3 {
		fmt.Fprintln(c.term, "usage: /stats object <id>")
		return nil
	}
	objectID := ctx.parts[2]
	obj := ctx.stats.ObjectExecSnapshot(objectID)
	if obj == nil {
		fmt.Fprintf(c.term, "No stats for object %q\n", objectID)
		return nil
	}

	fmt.Fprintf(c.term, "Object: %s\n", obj.ObjectID)
	fmt.Fprintf(c.term, "Source: %s\n", obj.SourcePath)
	fmt.Fprintf(c.term, "Executions: %d\n", obj.Executions)
	fmt.Fprintf(c.term, "Time: avg=%.1fms, min=%.1fms, max=%.1fms\n",
		obj.AvgTimeMs, obj.MinTimeMs, obj.MaxTimeMs)
	fmt.Fprintf(c.term, "Slow: %d (%.2f%%)\n", obj.SlowCount, obj.SlowPercent)
	fmt.Fprintf(c.term, "Errors: %d (%.2f%%)\n", obj.Errors, obj.ErrorPercent)
	if !obj.LastExecution.IsZero() {
		fmt.Fprintf(c.term, "Last execution: %s\n", obj.LastExecution.Format(time.RFC3339))
	}
	if !obj.LastError.IsZero() {
		fmt.Fprintf(c.term, "Last error: %s\n", obj.LastError.Format(time.RFC3339))
	}
	fmt.Fprintf(c.term, "\nExecution rates: %.2f/s, %.1f/m, %.0f/h\n",
		obj.ExecRates.PerSecond, obj.ExecRates.PerMinute, obj.ExecRates.PerHour)
	fmt.Fprintf(c.term, "Time rates: %.3fs/s, %.1fs/m, %.0fs/h\n",
		obj.TimeRates.PerSecond, obj.TimeRates.PerMinute, obj.TimeRates.PerHour)
	fmt.Fprintf(c.term, "Error rates: %.2f/s, %.1f/m, %.0f/h\n",
		obj.ErrorRates.PerSecond, obj.ErrorRates.PerMinute, obj.ErrorRates.PerHour)
	if len(obj.ByCategory) > 0 {
		fmt.Fprintln(c.term, "\nErrors by category:")
		for cat, count := range obj.ByCategory {
			fmt.Fprintf(c.term, "  %s: %d\n", cat, count)
		}
	}
	if len(obj.ByLocation) > 0 {
		fmt.Fprintln(c.term, "\nErrors by location:")
		for loc, count := range obj.ByLocation {
			fmt.Fprintf(c.term, "  %s: %d\n", loc, count)
		}
	}
	if len(obj.ByScript) > 0 {
		fmt.Fprintln(c.term, "\nErrors by script:")
		for script, count := range obj.ByScript {
			fmt.Fprintf(c.term, "  %s: %d\n", script, count)
		}
	}
	return nil
}

// Intervals subcommand handler

var intervalSortMap = map[string]IntervalSortField{
	"time":      SortIntervalByTime,
	"execs":     SortIntervalByExecs,
	"slow":      SortIntervalBySlow,
	"errors":    SortIntervalByErrors,
	"errorrate": SortIntervalByErrorRate,
}

func handleStatsIntervals(ctx *statsContext) error {
	parts := ctx.parts
	sortBy := SortIntervalByTime
	n := 20

	// Parse sort or n from parts[2]
	if len(parts) >= 3 {
		if sort, ok := intervalSortMap[parts[2]]; ok {
			sortBy = sort
		} else if parsed, err := strconv.Atoi(parts[2]); err == nil && parsed > 0 {
			n = parsed
		}
	}

	// Parse n from parts[3] if parts[2] was a sort key
	if len(parts) >= 4 {
		if parsed, err := strconv.Atoi(parts[3]); err == nil && parsed > 0 {
			n = parsed
		}
	}

	intervals := ctx.stats.TopIntervals(sortBy, n)
	if len(intervals) == 0 {
		fmt.Fprintln(ctx.c.term, "No interval executions recorded.")
		return nil
	}

	t := table.New("Interval ID", "Object ID", "Event", "Execs", "Avg(ms)", "Slow%", "Errs").WithWriter(ctx.c.term)
	for _, iv := range intervals {
		t.AddRow(
			iv.IntervalID,
			iv.ObjectID,
			iv.EventName,
			iv.Executions,
			fmt.Sprintf("%.1f", iv.AvgTimeMs),
			fmt.Sprintf("%.1f", iv.SlowPercent),
			iv.Errors,
		)
	}
	t.Print()
	return nil
}

// Users subcommand handler

var userFilterMap = map[string]storage.UserFilter{
	"all":     storage.UserFilterAll,
	"owners":  storage.UserFilterOwners,
	"wizards": storage.UserFilterWizards,
	"players": storage.UserFilterPlayers,
}

var userSortMap = map[string]storage.UserSortField{
	"name":   storage.UserSortByName,
	"id":     storage.UserSortByID,
	"login":  storage.UserSortByLastLogin,
	"recent": storage.UserSortByLastLogin,
	"stale":  storage.UserSortByLastLoginAsc,
	"oldest": storage.UserSortByLastLoginAsc,
}

func handleStatsUsers(ctx *statsContext) error {
	c := ctx.c
	parts := ctx.parts
	filter := storage.UserFilterAll
	sortBy := storage.UserSortByName
	n := 20
	argIdx := 2

	// Parse filter (optional)
	if len(parts) > argIdx {
		if f, ok := userFilterMap[parts[argIdx]]; ok {
			filter = f
			argIdx++
		}
	}

	// Parse sort (optional)
	if len(parts) > argIdx {
		if s, ok := userSortMap[parts[argIdx]]; ok {
			sortBy = s
			argIdx++
		} else if parsed, err := strconv.Atoi(parts[argIdx]); err == nil && parsed > 0 {
			n = parsed
			argIdx++
		}
	}

	// Parse limit (optional)
	if len(parts) > argIdx {
		if parsed, err := strconv.Atoi(parts[argIdx]); err == nil && parsed > 0 {
			n = parsed
		}
	}

	users, err := c.game.storage.ListUsers(c.ctx, filter, sortBy, n)
	if err != nil {
		fmt.Fprintf(c.term, "Error: %v\n", err)
		return nil
	}
	if len(users) == 0 {
		fmt.Fprintln(c.term, "No users found.")
		return nil
	}

	total, _ := c.game.storage.CountUsers(c.ctx, filter)
	fmt.Fprintf(c.term, "Users (%d shown, %d total):\n", len(users), total)

	t := table.New("Name", "Role", "Object", "Last Login", "Online").WithWriter(c.term)
	for _, user := range users {
		role := ""
		if user.Owner {
			role = "owner"
		} else if user.Wizard {
			role = "wizard"
		}

		online := ""
		if _, found := c.game.connectionByObjectID.GetHas(user.Object); found {
			online = "*"
		}

		lastLoginStr := "never"
		if !user.LastLogin().IsZero() {
			lastLoginStr = user.LastLogin().Format("2006-01-02 15:04")
		}

		t.AddRow(user.Name, role, user.Object, lastLoginStr, online)
	}
	t.Print()
	return nil
}

// Flush subcommand handler

func handleStatsFlush(ctx *statsContext) error {
	c := ctx.c
	health := c.game.storage.FlushHealth()

	if health.Healthy() {
		fmt.Fprintln(c.term, "Flush status: OK")
		if !health.LastFlushAt.IsZero() {
			fmt.Fprintf(c.term, "Last successful flush: %v ago\n", time.Since(health.LastFlushAt).Truncate(time.Second))
		}
	} else {
		fmt.Fprintln(c.term, "Flush status: FAILING")
		if !health.LastFlushAt.IsZero() {
			fmt.Fprintf(c.term, "Last successful flush: %v ago\n", time.Since(health.LastFlushAt).Truncate(time.Second))
		}
		if health.LastError != nil {
			fmt.Fprintf(c.term, "Last error: %v\n", health.LastError)
		}
		if !health.LastErrorAt.IsZero() {
			fmt.Fprintf(c.term, "Last failure: %v ago\n", time.Since(health.LastErrorAt).Truncate(time.Second))
		}
	}
	return nil
}

// Reset subcommand handler

func handleStatsReset(ctx *statsContext) error {
	ctx.stats.Reset()
	fmt.Fprintln(ctx.c.term, "Statistics reset.")
	return nil
}

// executeStatsCommand is the main entry point for the /stats command.
func executeStatsCommand(c *Connection, parts []string) error {
	stats := c.game.jsStats

	subcmd := "summary"
	if len(parts) >= 2 {
		subcmd = parts[1]
	}

	ctx := &statsContext{
		c:     c,
		stats: stats,
		parts: parts,
	}

	if sub, ok := statsSubcommands[subcmd]; ok {
		return sub.handler(ctx)
	}

	printStatsHelp(c.term)
	return nil
}
