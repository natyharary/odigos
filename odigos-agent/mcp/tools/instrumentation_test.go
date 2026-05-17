package tools

import (
	"strings"
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/instrumentationrules"
	"github.com/odigos-io/odigos/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUnifiedDiffLinesAddOnly(t *testing.T) {
	got := UnifiedDiffLines("", "alpha\nbeta\n")
	want := "+ alpha\n+ beta\n"
	if got != want {
		t.Fatalf("greenfield diff mismatch: got %q want %q", got, want)
	}
}

func TestUnifiedDiffLinesContextAndChange(t *testing.T) {
	before := "alpha\nbeta\n"
	after := "alpha\ngamma\n"
	got := UnifiedDiffLines(before, after)
	// beta -> gamma at index 1, alpha is context.
	if got != "  alpha\n- beta\n+ gamma\n" {
		t.Fatalf("mid-line change diff mismatch: got %q", got)
	}
}

func TestUnifiedDiffLinesRemoveOnly(t *testing.T) {
	got := UnifiedDiffLines("alpha\nbeta\n", "")
	want := "- alpha\n- beta\n"
	if got != want {
		t.Fatalf("delete-only diff mismatch: got %q want %q", got, want)
	}
}

func TestPickManagedRulePicksWorkloadScopedWithOtelDistros(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	userRule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "user-pinned-distro"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads: &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{
				OtelDistroNames: []string{"opentelemetry-ebpf-instrumentation"},
			},
		},
	}
	got := pickManagedRule([]odigosv1.InstrumentationRule{userRule}, "ns", "Deployment", "app")
	if got == nil || got.Name != "user-pinned-distro" {
		t.Fatalf("expected to pick the user-authored rule for patching, got %v", got)
	}
}

func TestPickManagedRulePrefersAgentPrefix(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	userRule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "user-rule"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"java-community"}},
		},
	}
	agentRule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: instrumentationRuleNamePrefix + "abc123"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"opentelemetry-ebpf-instrumentation"}},
		},
	}
	got := pickManagedRule([]odigosv1.InstrumentationRule{userRule, agentRule}, "ns", "Deployment", "app")
	if got == nil || got.Name != agentRule.Name {
		t.Fatalf("expected agent-prefixed rule to win; got %v", got)
	}
}

func TestPickManagedRuleIgnoresMultiWorkloadRule(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{
		{Namespace: "ns", Name: "app", Kind: k8sconsts.WorkloadKindDeployment},
		{Namespace: "ns", Name: "other", Kind: k8sconsts.WorkloadKindDeployment},
	}
	rule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "broad-rule"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"java-community"}},
		},
	}
	if got := pickManagedRule([]odigosv1.InstrumentationRule{rule}, "ns", "Deployment", "app"); got != nil {
		t.Fatalf("expected nil for multi-workload rule; got %v", got.Name)
	}
}

func TestPickManagedRuleIgnoresWorkloadRuleWithoutOtelDistros(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	payloadOnlyRule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "payload-only"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:         &workloads,
			PayloadCollection: &instrumentationrules.PayloadCollection{},
		},
	}
	if got := pickManagedRule([]odigosv1.InstrumentationRule{payloadOnlyRule}, "ns", "Deployment", "app"); got != nil {
		t.Fatalf("expected nil when no OtelDistros pinned; got %v", got.Name)
	}
}

func TestBuildDesiredRuleGreenfieldCarriesManagedByLabel(t *testing.T) {
	rule := buildDesiredRule(nil, "ns", "Deployment", "app", common.JavaProgrammingLanguage, "java-community", "override_distro")
	if rule == nil {
		t.Fatal("expected greenfield rule, got nil")
	}
	if rule.GenerateName != instrumentationRuleNamePrefix {
		t.Fatalf("expected GenerateName %q, got %q", instrumentationRuleNamePrefix, rule.GenerateName)
	}
	if got := rule.Labels[managedByLabelKey]; got != managedByLabelValue {
		t.Fatalf("missing managed-by label: got %q", got)
	}
	if rule.Spec.Workloads == nil || len(*rule.Spec.Workloads) != 1 {
		t.Fatalf("expected single workload binding, got %+v", rule.Spec.Workloads)
	}
	if rule.Spec.OtelDistros == nil || len(rule.Spec.OtelDistros.OtelDistroNames) != 1 ||
		rule.Spec.OtelDistros.OtelDistroNames[0] != "java-community" {
		t.Fatalf("expected OtelDistroNames=[java-community]; got %+v", rule.Spec.OtelDistros)
	}
}

func TestBuildDesiredRulePatchesExistingInPlace(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	existing := &odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "user-rule", Namespace: "ns"},
		Spec: odigosv1.InstrumentationRuleSpec{
			RuleName:    "user-rule",
			Notes:       "original notes",
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"opentelemetry-ebpf-instrumentation"}},
		},
	}
	rule := buildDesiredRule(existing, "ns", "Deployment", "app", common.JavaProgrammingLanguage, "java-community", "override_distro")
	if rule.Name != "user-rule" {
		t.Fatalf("expected to preserve existing rule name, got %q", rule.Name)
	}
	if rule.Spec.Notes != "original notes" {
		t.Fatalf("expected to preserve user notes, got %q", rule.Spec.Notes)
	}
	if rule.Spec.OtelDistros.OtelDistroNames[0] != "java-community" {
		t.Fatalf("expected OtelDistroNames to be rewritten to [java-community], got %+v", rule.Spec.OtelDistros.OtelDistroNames)
	}
	if existing.Spec.OtelDistros.OtelDistroNames[0] != "opentelemetry-ebpf-instrumentation" {
		t.Fatalf("expected source rule untouched, got %+v", existing.Spec.OtelDistros.OtelDistroNames)
	}
}

func TestRollbackHintForGreenfieldUsesManagedByLabel(t *testing.T) {
	plan := &rulePlan{Namespace: "ns"}
	hint := rollbackHintFor(plan)
	if !strings.Contains(hint, managedByLabelKey+"="+managedByLabelValue) {
		t.Fatalf("expected hint to reference the managed-by selector; got %q", hint)
	}
}

func TestResolveDistroForContainerFiltersByLanguage(t *testing.T) {
	// A rule pinning [java-community] should NOT be reported as the
	// active distro for a Go container - the instrumentor would not apply
	// it either.
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	rule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-language-pin"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"java-community"}},
		},
	}
	config := &odigosv1.InstrumentationConfig{
		Spec: odigosv1.InstrumentationConfigSpec{},
	}
	defaults := map[common.ProgrammingLanguage]string{common.GoProgrammingLanguage: "golang-community"}
	distroLanguage := func(name string) common.ProgrammingLanguage {
		if name == "java-community" {
			return common.JavaProgrammingLanguage
		}
		if name == "opentelemetry-ebpf-instrumentation" {
			return common.ProgrammingLanguageWildcard
		}
		return ""
	}
	got, source := resolveDistroForContainer(config, []odigosv1.InstrumentationRule{rule}, defaults, "demo", common.GoProgrammingLanguage, distroLanguage)
	if got != "golang-community" || source != "default" {
		t.Fatalf("expected go workload to fall through to default golang-community; got %q (%s)", got, source)
	}
}

func TestResolveDistroForContainerAcceptsWildcard(t *testing.T) {
	workloads := []k8sconsts.PodWorkload{{
		Namespace: "ns",
		Name:      "app",
		Kind:      k8sconsts.WorkloadKindDeployment,
	}}
	rule := odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "ebpf-pin"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Workloads:   &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{OtelDistroNames: []string{"opentelemetry-ebpf-instrumentation"}},
		},
	}
	defaults := map[common.ProgrammingLanguage]string{common.JavaProgrammingLanguage: "java-community"}
	distroLanguage := func(name string) common.ProgrammingLanguage {
		if name == "opentelemetry-ebpf-instrumentation" {
			return common.ProgrammingLanguageWildcard
		}
		return common.JavaProgrammingLanguage
	}
	got, source := resolveDistroForContainer(&odigosv1.InstrumentationConfig{}, []odigosv1.InstrumentationRule{rule}, defaults, "demo", common.JavaProgrammingLanguage, distroLanguage)
	if got != "opentelemetry-ebpf-instrumentation" || source != "rule:ebpf-pin" {
		t.Fatalf("expected wildcard distro to match Java workload; got %q (%s)", got, source)
	}
}

