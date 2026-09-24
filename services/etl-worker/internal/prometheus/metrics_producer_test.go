package prometheus

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// moduleRoot is the Go module root (services/etl-worker) relative to this
// package. Producers of these metrics live in cmd/worker and cmd/api, so the
// scan has to cover the whole module: scanning only this package reports
// DLQMessages and ESDeadLetter as dead, and both are fed from cmd/worker.
const moduleRoot = "../.."

// TestEveryMetricFieldHasAProducer is the permanent form of a defect that took
// three attempts to find by hand.
//
// ai_etl_pipeline_chunks_processed_total was declared, constructed and
// registered - and never fed. A CounterVec with no children exports nothing at
// all, so the family did not appear in a single scrape: anyone who wrote a query
// against it got an empty result, and then had to work out whether the pipeline
// was idle or the metric was dead. It was deleted rather than wired, because the
// per-chunk count already exists as
// ai_etl_pipeline_stage_duration_seconds_count{stage="store"} (observed once per
// chunk, pipeline.go:966), and a second counter for one event is how the two
// drift apart.
//
// The rule: the field must be dereferenced somewhere in the module's non-test
// sources (`.Field.` for a vec, `.Field)` for a value handed over as an
// argument). Registration does not count - it writes `m.Field,` - which is
// exactly why "is the field referenced anywhere" is the wrong question: that is
// true for every metric, including the dead ones.
//
// Known limitation, stated so that a green run is not read as a stronger claim:
// this catches "no producer anywhere", not "a producer nobody calls".
// CircuitState is the second shape - it has SetCircuitState, and what keeps it
// alive is a method-value handoff in cmd/worker
// (circuit.SetStateObserver(prom.SetCircuitState)). Catching that needs a call
// graph, which is out of reach for a test this size. If a future metric is
// produced through a form this pattern does not cover, the test fails loudly
// rather than silently passing, so widen the pattern instead of deleting the
// check.
func TestEveryMetricFieldHasAProducer(t *testing.T) {
	sources, err := moduleGoSources(moduleRoot)
	if err != nil {
		t.Fatalf("collect module sources: %v", err)
	}
	if len(sources) < 50 {
		t.Fatalf("only %d non-test sources found under %s; the walk is probably "+
			"looking in the wrong place, and a short list makes this test pass "+
			"for the wrong reason", len(sources), moduleRoot)
	}

	metricsType := reflect.TypeOf(Metrics{})
	checked := 0
	var dead []string
	for i := 0; i < metricsType.NumField(); i++ {
		field := metricsType.Field(i)
		if !isPrometheusMetric(field.Type) {
			continue
		}
		checked++
		if !hasProducer(sources, field.Name) {
			dead = append(dead, field.Name)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d metric fields seen on Metrics; the type filter is "+
			"probably wrong, which would make this test pass by checking nothing",
			checked)
	}
	if len(dead) > 0 {
		t.Fatalf("metric fields with no producer: %v\n"+
			"Each of these is registered but never fed, so it exports no series "+
			"at all while looking like a signal that exists. Wire it or delete "+
			"it - do not leave it.", dead)
	}
}

// isPrometheusMetric reports whether a struct field holds a metric type from the
// Prometheus client library, as opposed to the mutex and map that share the
// struct.
func isPrometheusMetric(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath() == "github.com/prometheus/client_golang/prometheus"
}

// hasProducer reports whether any source dereferences the field.
func hasProducer(sources []string, field string) bool {
	pattern := regexp.MustCompile(`\.` + regexp.QuoteMeta(field) + `[.)]`)
	for _, src := range sources {
		if pattern.MatchString(src) {
			return true
		}
	}
	return false
}

// moduleGoSources reads every non-test Go source under root, skipping hidden
// directories and vendor. The hidden ones hold tool caches (.gomod/.gocache)
// whose file count dwarfs the first-party code, and vendor holds third-party
// code whose field names must not be able to satisfy this check.
func moduleGoSources(root string) ([]string, error) {
	var sources []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources = append(sources, string(content))
		return nil
	})
	return sources, err
}
