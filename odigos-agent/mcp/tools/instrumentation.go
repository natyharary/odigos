package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/instrumentationrules"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/distros"
	"github.com/odigos-io/odigos/distros/distro"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// obiUniversalDistroName is the wildcard eBPF distro that covers every language
// odigos has not shipped a language-native eBPF distro for. Go has its own
// (`golang-community`); everything else falls back to this name.
const obiUniversalDistroName = "opentelemetry-ebpf-instrumentation"

// instrumentationRuleNamePrefix tags rules the agent creates so they're easy
// to recognise in `kubectl get` and rollback hints.
const instrumentationRuleNamePrefix = "odigos-agent-"

// managedByLabelKey / managedByLabelValue tag every rule the agent creates so
// the rollback `kubectl delete -l ...` hint actually matches them. Patched
// pre-existing rules are not relabelled - that would silently broaden the
// rollback hint to delete user-authored rules.
const managedByLabelKey = "app.kubernetes.io/managed-by"
const managedByLabelValue = "odigos-agent"

// RegisterInstrumentationTools wires the distro / OBI override MCP tools.
// Loads the embedded distro registry at startup; the YAMLs are baked into the
// binary so the only failure mode is a programmer error in the distros package.
func RegisterInstrumentationTools(server *mcpserver.MCPServer, clients *Clients, approvals *ApprovalCache) {
	getter, err := distros.NewCommunityGetter()
	if err != nil {
		log.Fatalf("load embedded distro registry: %v", err)
	}
	ebpfByLanguage := map[common.ProgrammingLanguage]string{}
	for _, entry := range getter.GetAllDistros() {
		if !entry.IsEbpf {
			continue
		}
		if _, exists := ebpfByLanguage[entry.Language]; !exists {
			ebpfByLanguage[entry.Language] = entry.Name
		}
	}
	manager := &instrumentationManager{
		clients:        clients,
		approvals:      approvals,
		distros:        getter,
		defaulter:      distros.NewCommunityDefaulter(),
		ebpfByLanguage: ebpfByLanguage,
	}
	manager.register(server)
}

type instrumentationManager struct {
	clients        *Clients
	approvals      *ApprovalCache
	distros        *distros.Getter
	defaulter      distros.Defaulter
	ebpfByLanguage map[common.ProgrammingLanguage]string
}

func (m *instrumentationManager) register(server *mcpserver.MCPServer) {
	server.AddTool(mcp.NewTool(
		"list_available_distros",
		mcp.WithDescription("List every otel distro the in-cluster odigos installation can use, with their language, eBPF flag, supported signals, and a short description. Filter by language with the optional `language` argument."),
		mcp.WithString("language", mcp.Description("Optional language filter (e.g. java, python, go, dotnet, javascript, php, ruby).")),
	), m.listAvailableDistros)

	server.AddTool(mcp.NewTool(
		"get_workload_distro",
		mcp.WithDescription("Resolve the currently-applied otel distro per container for a workload, plus where the choice came from (container override, matching InstrumentationRule, or odigos default for the detected language)."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
		mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
	), m.getWorkloadDistro)

	server.AddTool(mcp.NewTool(
		"check_obi_eligibility",
		mcp.WithDescription("Report whether OBI (eBPF) instrumentation can be enabled for a workload. Currently checks that runtime detection has identified at least one language and that an eBPF distro exists for it. Does not probe per-node kernel versions."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
		mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
	), m.checkObiEligibility)

	server.AddTool(mcp.NewTool(
		"propose_override_distro",
		mcp.WithDescription("Server-side dry-run patch (or create) of an InstrumentationRule that pins the workload's language to the named distro. Returns request_id + before/after YAML + diff. Caller must invoke apply_override_distro with the request_id after a user approves."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
		mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		mcp.WithString("distro_name", mcp.Required(), mcp.Description("Target distro name (must exist in list_available_distros and match the workload's detected language).")),
	), m.proposeOverrideDistro)
	server.AddTool(mcp.NewTool(
		"apply_override_distro",
		mcp.WithDescription("Apply a previously-proposed override_distro mutation. request_id must match a recent propose_override_distro call."),
		mcp.WithString("request_id", mcp.Required(), mcp.Description("The request_id returned by propose_override_distro.")),
	), m.applyOverrideDistro)

	server.AddTool(mcp.NewTool(
		"propose_enable_obi",
		mcp.WithDescription("Server-side dry-run patch (or create) of an InstrumentationRule that switches the workload to an eBPF/OBI distro. Picks the language-native eBPF distro when available, otherwise the universal `"+obiUniversalDistroName+"`. Returns request_id + before/after YAML + diff. Caller must invoke apply_enable_obi after a user approves."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
		mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
	), m.proposeEnableObi)
	server.AddTool(mcp.NewTool(
		"apply_enable_obi",
		mcp.WithDescription("Apply a previously-proposed enable_obi mutation. request_id must match a recent propose_enable_obi call."),
		mcp.WithString("request_id", mcp.Required(), mcp.Description("The request_id returned by propose_enable_obi.")),
	), m.applyEnableObi)

	server.AddTool(mcp.NewTool(
		"propose_disable_obi",
		mcp.WithDescription("Server-side dry-run patch of the InstrumentationRule managing this workload, rewriting OtelDistroNames to the language's default SDK distro so the workload falls back from eBPF/OBI to the SDK. Returns request_id + before/after YAML + diff. Caller must invoke apply_disable_obi after a user approves."),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
		mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
	), m.proposeDisableObi)
	server.AddTool(mcp.NewTool(
		"apply_disable_obi",
		mcp.WithDescription("Apply a previously-proposed disable_obi mutation. request_id must match a recent propose_disable_obi call."),
		mcp.WithString("request_id", mcp.Required(), mcp.Description("The request_id returned by propose_disable_obi.")),
	), m.applyDisableObi)
}

// ---- read handlers ----

func (m *instrumentationManager) listAvailableDistros(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	languageFilter := strings.TrimSpace(request.GetString("language", ""))
	all := m.distros.GetAllDistros()
	entries := make([]map[string]any, 0, len(all))
	for _, distroEntry := range all {
		if languageFilter != "" && string(distroEntry.Language) != languageFilter && distroEntry.Language != common.ProgrammingLanguageWildcard {
			continue
		}
		entries = append(entries, distroSummary(distroEntry))
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i]["name"].(string) < entries[j]["name"].(string)
	})
	return WriteJSON(map[string]any{
		"language_filter": languageFilter,
		"distros":         entries,
		"count":           len(entries),
	})
}

func (m *instrumentationManager) getWorkloadDistro(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace, kind, name, errResult, ok := RequireWorkloadArgs(request)
	if !ok {
		return errResult, nil
	}

	config, found, err := m.getInstrumentationConfig(ctx, namespace, kind, name)
	if err != nil {
		return ToolError("read InstrumentationConfig for %s/%s/%s: %v", namespace, kind, name, err)
	}
	if !found {
		return WriteJSON(map[string]any{
			"found":  false,
			"reason": "instrumentation_config_not_found",
		})
	}

	rules, err := m.matchingRules(ctx, namespace, kind, name)
	if err != nil {
		return ToolError("list InstrumentationRules for %s/%s/%s: %v", namespace, kind, name, err)
	}
	defaults := m.defaulter.GetDefaultDistroNames()

	containers := make([]map[string]any, 0)
	languages := map[string]struct{}{}
	for _, runtimeDetails := range config.Status.RuntimeDetailsByContainer {
		language := runtimeDetails.Language
		languages[string(language)] = struct{}{}
		distroName, source := resolveDistroForContainer(config, rules, defaults, runtimeDetails.ContainerName, language, m.distroLanguage)
		containers = append(containers, map[string]any{
			"container":   runtimeDetails.ContainerName,
			"language":    string(language),
			"distro_name": distroName,
			"source":      source,
			"is_ebpf":     m.isEbpfDistro(distroName),
		})
	}

	languageList := make([]string, 0, len(languages))
	for language := range languages {
		languageList = append(languageList, language)
	}
	sort.Strings(languageList)

	return WriteJSON(map[string]any{
		"found":      true,
		"namespace":  namespace,
		"kind":       kind,
		"name":       name,
		"languages":  languageList,
		"containers": containers,
	})
}

func (m *instrumentationManager) checkObiEligibility(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace, kind, name, errResult, ok := RequireWorkloadArgs(request)
	if !ok {
		return errResult, nil
	}

	config, found, err := m.getInstrumentationConfig(ctx, namespace, kind, name)
	if err != nil {
		return ToolError("read InstrumentationConfig for %s/%s/%s: %v", namespace, kind, name, err)
	}
	if !found {
		return WriteJSON(map[string]any{
			"eligible": false,
			"reasons":  []string{"instrumentation_config_not_found"},
		})
	}

	reasons := []string{}
	languages := uniqueLanguages(config.Status.RuntimeDetailsByContainer)
	if len(languages) == 0 {
		reasons = append(reasons, "runtime_language_not_detected")
	}

	perLanguage := make([]map[string]any, 0, len(languages))
	anyEligible := false
	for _, language := range languages {
		ebpfDistro := m.ebpfDistroForLanguage(language)
		entry := map[string]any{
			"language":    string(language),
			"ebpf_distro": ebpfDistro,
			"eligible":    ebpfDistro != "",
		}
		if ebpfDistro == "" {
			entry["reason"] = "no_ebpf_distro_for_language"
		} else {
			anyEligible = true
		}
		perLanguage = append(perLanguage, entry)
	}

	if !anyEligible && len(languages) > 0 {
		reasons = append(reasons, "no_ebpf_distro_for_any_detected_language")
	}

	return WriteJSON(map[string]any{
		"eligible":            anyEligible,
		"reasons":             reasons,
		"languages":           stringifyLanguages(languages),
		"per_language":        perLanguage,
		"kernel_check_skipped": true, // node-level probe is out of scope; see DECISIONS.md ADR-017
	})
}

// ---- mutation handlers ----

func (m *instrumentationManager) proposeOverrideDistro(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace, kind, name, errResult, ok := RequireWorkloadArgs(request)
	if !ok {
		return errResult, nil
	}
	targetDistroName, err := request.RequireString("distro_name")
	if err != nil {
		return ToolError("distro_name required: %v", err)
	}
	plan, planErr := m.planDistroChange(ctx, namespace, kind, name, "override_distro", m.makePicker("override_distro", targetDistroName))
	if planErr != nil {
		return ToolError("propose_override_distro %s/%s/%s: %v", namespace, kind, name, planErr)
	}
	return m.cacheAndRespond(plan, "propose_override_distro")
}

func (m *instrumentationManager) proposeEnableObi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace, kind, name, errResult, ok := RequireWorkloadArgs(request)
	if !ok {
		return errResult, nil
	}
	plan, planErr := m.planDistroChange(ctx, namespace, kind, name, "enable_obi", m.makePicker("enable_obi", ""))
	if planErr != nil {
		return ToolError("propose_enable_obi %s/%s/%s: %v", namespace, kind, name, planErr)
	}
	return m.cacheAndRespond(plan, "propose_enable_obi")
}

func (m *instrumentationManager) proposeDisableObi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	namespace, kind, name, errResult, ok := RequireWorkloadArgs(request)
	if !ok {
		return errResult, nil
	}
	plan, planErr := m.planDistroChange(ctx, namespace, kind, name, "disable_obi", m.makePicker("disable_obi", ""))
	if planErr != nil {
		return ToolError("propose_disable_obi %s/%s/%s: %v", namespace, kind, name, planErr)
	}
	return m.cacheAndRespond(plan, "propose_disable_obi")
}

// makePicker returns the op-specific function that derives the target distro
// from the current state. The picker is parameterized only on the op and (for
// override_distro) the user-requested target name; for the OBI ops the target
// derives from the workload's detected language at picker invocation time.
//
// Both propose and apply run the picker against fresh live state. That means
// apply re-validates the same invariants (target distro still in registry +
// matches the language, current still differs from target, current is still
// eBPF for disable_obi) using whatever the cluster looks like at apply time,
// not the cached dry-run snapshot. A user editing the rule between propose
// and apply surfaces as a "nothing to override" / "not eBPF - nothing to
// disable" error rather than a silent corrective rewrite.
func (m *instrumentationManager) makePicker(op, targetDistroName string) func(language common.ProgrammingLanguage, current string) (string, error) {
	switch op {
	case "override_distro":
		return func(language common.ProgrammingLanguage, current string) (string, error) {
			target := m.distros.GetDistroByName(targetDistroName)
			if target == nil {
				return "", fmt.Errorf("distro %q not found in registry", targetDistroName)
			}
			if target.Language != language && target.Language != common.ProgrammingLanguageWildcard {
				return "", fmt.Errorf("distro %q is for language %q, workload language is %q", targetDistroName, target.Language, language)
			}
			if current == targetDistroName {
				return "", fmt.Errorf("workload already uses distro %q for language %q - nothing to override", targetDistroName, language)
			}
			return targetDistroName, nil
		}
	case "enable_obi":
		return func(language common.ProgrammingLanguage, current string) (string, error) {
			target := m.ebpfDistroForLanguage(language)
			if target == "" {
				return "", fmt.Errorf("no eBPF distro available for language %q", language)
			}
			if current == target {
				return "", fmt.Errorf("workload already uses eBPF distro %q for language %q", target, language)
			}
			return target, nil
		}
	case "disable_obi":
		return func(language common.ProgrammingLanguage, current string) (string, error) {
			if !m.isEbpfDistro(current) {
				return "", fmt.Errorf("workload's current distro %q for language %q is not eBPF - nothing to disable", current, language)
			}
			defaults := m.defaulter.GetDefaultDistroNames()
			fallback, hasDefault := defaults[language]
			if !hasDefault {
				return "", fmt.Errorf("no default SDK distro registered for language %q", language)
			}
			return fallback, nil
		}
	}
	return func(_ common.ProgrammingLanguage, _ string) (string, error) {
		return "", fmt.Errorf("unknown op: %s", op)
	}
}

func (m *instrumentationManager) applyOverrideDistro(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return m.applyDistroChange(ctx, request, "override_distro")
}

func (m *instrumentationManager) applyEnableObi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return m.applyDistroChange(ctx, request, "enable_obi")
}

func (m *instrumentationManager) applyDisableObi(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return m.applyDistroChange(ctx, request, "disable_obi")
}

// ---- shared mutation plumbing ----

// rulePlan captures the dry-run output of a proposed distro change. Each
// propose_* handler builds one; cacheAndRespond stores it under a request_id
// and serialises the preview for the agent. The existence-check round-trip
// against the apps/batch API is elided here: getInstrumentationConfig's
// NotFound path already implies the workload isn't being tracked by odigos.
type rulePlan struct {
	Op           string
	Namespace    string
	WorkloadKind string
	WorkloadName string
	Language     common.ProgrammingLanguage
	FromDistro   string
	ToDistro     string
	ExistingRule *odigosv1.InstrumentationRule
	DesiredRule  *odigosv1.InstrumentationRule
	YAMLBefore   string
	YAMLAfter    string
	Diff         string
	RollbackHint string
}

func (m *instrumentationManager) planDistroChange(
	ctx context.Context,
	namespace, kind, name, op string,
	pickDistro func(language common.ProgrammingLanguage, current string) (string, error),
) (*rulePlan, error) {
	if !IsSupportedWorkloadKind(kind) {
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
	if k8sconsts.WorkloadKind(kind) == k8sconsts.WorkloadKindNamespace {
		return nil, fmt.Errorf("namespace-scoped workloads not supported for distro overrides")
	}

	config, found, err := m.getInstrumentationConfig(ctx, namespace, kind, name)
	if err != nil {
		return nil, fmt.Errorf("read InstrumentationConfig: %v", err)
	}
	if !found {
		return nil, fmt.Errorf("InstrumentationConfig not found - workload is not instrumented yet; create a Source first")
	}
	languages := uniqueLanguages(config.Status.RuntimeDetailsByContainer)
	if len(languages) == 0 {
		return nil, fmt.Errorf("runtime language not detected yet")
	}
	if len(languages) > 1 {
		return nil, fmt.Errorf("workload has multiple detected languages %v - single-language workloads only in phase 7", stringifyLanguages(languages))
	}
	language := languages[0]

	existingRules, err := m.matchingRules(ctx, namespace, kind, name)
	if err != nil {
		return nil, fmt.Errorf("list InstrumentationRules: %v", err)
	}
	currentDistro, _ := resolveLanguageDistro(config, existingRules, m.defaulter.GetDefaultDistroNames(), language, m.distroLanguage)

	targetDistro, err := pickDistro(language, currentDistro)
	if err != nil {
		return nil, err
	}

	managedRule := pickManagedRule(existingRules, namespace, kind, name)
	plan := &rulePlan{
		Op:           op,
		Namespace:    namespace,
		WorkloadKind: kind,
		WorkloadName: name,
		Language:     language,
		FromDistro:   currentDistro,
		ToDistro:     targetDistro,
		ExistingRule: managedRule,
		DesiredRule:  buildDesiredRule(managedRule, namespace, kind, name, language, targetDistro, op),
	}

	if err := m.dryRunRulePlan(ctx, plan); err != nil {
		return nil, err
	}
	plan.RollbackHint = rollbackHintFor(plan)
	return plan, nil
}

func (m *instrumentationManager) dryRunRulePlan(ctx context.Context, plan *rulePlan) error {
	client := m.clients.Odigos.OdigosV1alpha1().InstrumentationRules(plan.Namespace)

	if plan.ExistingRule != nil {
		yamlBytes, err := yaml.Marshal(stripRuleServerFields(plan.ExistingRule))
		if err != nil {
			return fmt.Errorf("marshal existing rule to YAML: %v", err)
		}
		plan.YAMLBefore = string(yamlBytes)
	}

	if plan.ExistingRule == nil {
		dryRun, err := client.Create(ctx, plan.DesiredRule, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return fmt.Errorf("server-side dry-run create InstrumentationRule: %v", err)
		}
		yamlBytes, err := yaml.Marshal(stripRuleServerFields(dryRun))
		if err != nil {
			return fmt.Errorf("marshal dry-run rule to YAML: %v", err)
		}
		plan.YAMLAfter = string(yamlBytes)
	} else {
		patchBytes, err := buildSpecMergePatch(plan.DesiredRule.Spec)
		if err != nil {
			return fmt.Errorf("build patch body: %v", err)
		}
		dryRun, err := client.Patch(ctx, plan.ExistingRule.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return fmt.Errorf("server-side dry-run patch InstrumentationRule %s: %v", plan.ExistingRule.Name, err)
		}
		yamlBytes, err := yaml.Marshal(stripRuleServerFields(dryRun))
		if err != nil {
			return fmt.Errorf("marshal dry-run rule to YAML: %v", err)
		}
		plan.YAMLAfter = string(yamlBytes)
	}

	plan.Diff = UnifiedDiffLines(plan.YAMLBefore, plan.YAMLAfter)
	return nil
}

func (m *instrumentationManager) cacheAndRespond(plan *rulePlan, auditOp string) (*mcp.CallToolResult, error) {
	requestID := m.approvals.Put(&PendingMutation{
		Operation:    plan.Op,
		Namespace:    plan.Namespace,
		WorkloadKind: plan.WorkloadKind,
		WorkloadName: plan.WorkloadName,
		YAMLBefore:   plan.YAMLBefore,
		YAMLAfter:    plan.YAMLAfter,
		Diff:         plan.Diff,
		RollbackHint: plan.RollbackHint,
		TargetDistro: plan.ToDistro,
	})
	log.Printf("audit: op=%s ns=%s kind=%s name=%s request_id=%s language=%s from=%s to=%s",
		auditOp, plan.Namespace, plan.WorkloadKind, plan.WorkloadName, requestID,
		plan.Language, plan.FromDistro, plan.ToDistro)
	return WriteJSON(map[string]any{
		"request_id":       requestID,
		"op":               plan.Op,
		"namespace":        plan.Namespace,
		"kind":             plan.WorkloadKind,
		"name":             plan.WorkloadName,
		"yaml_before":      plan.YAMLBefore,
		"yaml_after":       plan.YAMLAfter,
		"diff":             plan.Diff,
		"rollback_command": plan.RollbackHint,
		"context":          planContext(plan),
		"expires_in":       int(defaultApprovalTTL.Seconds()),
	})
}

func (m *instrumentationManager) applyDistroChange(ctx context.Context, request mcp.CallToolRequest, expectedOp string) (*mcp.CallToolResult, error) {
	requestID, err := request.RequireString("request_id")
	if err != nil {
		return ToolError("request_id required: %v", err)
	}
	pending := m.approvals.Take(requestID)
	if pending == nil {
		return ToolError("request_id %s not found, expired, or already consumed - call propose_%s again", requestID, expectedOp)
	}
	if pending.Operation != expectedOp {
		return ToolError("request_id %s is for operation %q, not %s", requestID, pending.Operation, expectedOp)
	}

	// Recompute the plan from scratch so live cluster state wins over cached bytes.
	plan, planErr := m.replanForApply(ctx, pending, expectedOp)
	if planErr != nil {
		log.Printf("audit: op=apply_%s request_id=%s result=replan_failed err=%v", expectedOp, requestID, planErr)
		return ToolError("re-plan %s %s/%s/%s: %v", expectedOp, pending.Namespace, pending.WorkloadKind, pending.WorkloadName, planErr)
	}

	result, err := m.executePlan(ctx, plan)
	if err != nil {
		log.Printf("audit: op=apply_%s request_id=%s result=failed err=%v", expectedOp, requestID, err)
		return ToolError("apply %s: %v", expectedOp, err)
	}
	log.Printf("audit: op=apply_%s request_id=%s result=%s rule=%s", expectedOp, requestID, result["result"], result["rule_name"])

	result["rollback_command"] = pending.RollbackHint
	return WriteJSON(result)
}

func (m *instrumentationManager) replanForApply(ctx context.Context, pending *PendingMutation, expectedOp string) (*rulePlan, error) {
	// Reuse the same picker the propose handler ran, so apply re-checks every
	// op-specific invariant against fresh live state. override_distro restores
	// the user-supplied target from the cached PendingMutation; the OBI ops
	// derive their target from the language at picker invocation time.
	if expectedOp == "override_distro" && pending.TargetDistro == "" {
		return nil, fmt.Errorf("cached pending mutation is missing TargetDistro for override_distro")
	}
	return m.planDistroChange(ctx, pending.Namespace, pending.WorkloadKind, pending.WorkloadName, expectedOp, m.makePicker(expectedOp, pending.TargetDistro))
}

func (m *instrumentationManager) executePlan(ctx context.Context, plan *rulePlan) (map[string]any, error) {
	client := m.clients.Odigos.OdigosV1alpha1().InstrumentationRules(plan.Namespace)

	if plan.ExistingRule == nil {
		created, err := client.Create(ctx, plan.DesiredRule, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("create InstrumentationRule: %v", err)
		}
		yamlBytes, _ := yaml.Marshal(stripRuleServerFields(created))
		return map[string]any{
			"result":      "created",
			"rule_name":   created.Name,
			"applied_yaml": string(yamlBytes),
		}, nil
	}

	patchBytes, err := buildSpecMergePatch(plan.DesiredRule.Spec)
	if err != nil {
		return nil, fmt.Errorf("build patch body: %v", err)
	}
	patched, err := client.Patch(ctx, plan.ExistingRule.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch InstrumentationRule %s: %v", plan.ExistingRule.Name, err)
	}
	yamlBytes, _ := yaml.Marshal(stripRuleServerFields(patched))
	return map[string]any{
		"result":       "patched",
		"rule_name":    patched.Name,
		"applied_yaml": string(yamlBytes),
	}, nil
}

// ---- helpers ----

func (m *instrumentationManager) getInstrumentationConfig(ctx context.Context, namespace, kind, name string) (*odigosv1.InstrumentationConfig, bool, error) {
	configName := workload.CalculateWorkloadRuntimeObjectName(name, kind)
	config, err := m.clients.Odigos.OdigosV1alpha1().InstrumentationConfigs(namespace).Get(ctx, configName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return config, true, nil
}

// matchingRules returns InstrumentationRules in the namespace that target the
// given workload. A nil Workloads pointer means "all workloads" - we include
// those too. An empty list means "no workloads" - we exclude.
func (m *instrumentationManager) matchingRules(ctx context.Context, namespace, kind, name string) ([]odigosv1.InstrumentationRule, error) {
	list, err := m.clients.Odigos.OdigosV1alpha1().InstrumentationRules(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	matched := make([]odigosv1.InstrumentationRule, 0, len(list.Items))
	for _, rule := range list.Items {
		if rule.Spec.Disabled {
			continue
		}
		if ruleTargetsWorkload(&rule, namespace, kind, name) {
			matched = append(matched, rule)
		}
	}
	return matched, nil
}

func ruleTargetsWorkload(rule *odigosv1.InstrumentationRule, namespace, kind, name string) bool {
	if rule.Spec.Workloads == nil {
		return true
	}
	for _, target := range *rule.Spec.Workloads {
		if string(target.Kind) == kind && target.Name == name && target.Namespace == namespace {
			return true
		}
	}
	return false
}

// pickManagedRule returns the rule the agent should patch when applying a
// distro change, or nil if a fresh rule must be created.
//
// "Managed" here means: scoped to exactly this workload (single-entry
// Workloads list) AND already pinning OtelDistros - i.e. a rule whose entire
// purpose is to pin a distro for this workload. We patch such rules in place
// (the user explicitly approves the before/after diff) so we never end up
// with two competing rules pinning different distros for the same workload.
//
// Rules with broader scope (Workloads is nil or covers multiple workloads),
// or workload-scoped rules with no OtelDistros set (carrying other features
// like PayloadCollection), are left untouched. In those cases the agent
// creates a new workload-scoped rule of its own.
//
// If multiple candidates match, agent-prefixed rules win to keep apply
// idempotent across repeated runs.
func pickManagedRule(rules []odigosv1.InstrumentationRule, namespace, kind, name string) *odigosv1.InstrumentationRule {
	var fallback *odigosv1.InstrumentationRule
	for index := range rules {
		rule := &rules[index]
		if rule.Spec.Workloads == nil || len(*rule.Spec.Workloads) != 1 {
			continue
		}
		target := (*rule.Spec.Workloads)[0]
		if string(target.Kind) != kind || target.Name != name || target.Namespace != namespace {
			continue
		}
		if rule.Spec.OtelDistros == nil {
			continue
		}
		if strings.HasPrefix(rule.Name, instrumentationRuleNamePrefix) {
			return rule
		}
		if fallback == nil {
			fallback = rule
		}
	}
	return fallback
}

// buildDesiredRule returns the rule we want post-mutation. All three ops pin
// OtelDistroNames to [targetDistro]; disable_obi differs only in that the
// target is the SDK default rather than an eBPF distro. We never delete a
// rule (so the agent never needs `delete` RBAC on instrumentationrules) -
// disable_obi rewrites the rule to point at the default SDK distro so the
// user sees an explicit record of the fallback.
func buildDesiredRule(existing *odigosv1.InstrumentationRule, namespace, kind, name string, language common.ProgrammingLanguage, targetDistro, op string) *odigosv1.InstrumentationRule {
	if existing != nil {
		desired := existing.DeepCopy()
		desired.Spec.OtelDistros = &instrumentationrules.OtelDistros{
			OtelDistroNames: []string{targetDistro},
		}
		return desired
	}

	notes := fmt.Sprintf("Created by odigos-agent via %s for %s %s/%s (language %s, distro %s).",
		op, kind, namespace, name, language, targetDistro)
	workloadRef := k8sconsts.PodWorkload{
		Namespace: namespace,
		Name:      name,
		Kind:      k8sconsts.WorkloadKind(kind),
	}
	workloads := []k8sconsts.PodWorkload{workloadRef}
	return &odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: instrumentationRuleNamePrefix,
			Namespace:    namespace,
			Labels: map[string]string{
				managedByLabelKey: managedByLabelValue,
			},
		},
		Spec: odigosv1.InstrumentationRuleSpec{
			RuleName:  fmt.Sprintf("agent-managed %s for %s/%s", op, kind, name),
			Notes:     notes,
			Workloads: &workloads,
			OtelDistros: &instrumentationrules.OtelDistros{
				OtelDistroNames: []string{targetDistro},
			},
		},
	}
}

func rollbackHintFor(plan *rulePlan) string {
	if plan.ExistingRule == nil {
		return fmt.Sprintf(
			"kubectl delete instrumentationrule -n %s -l %s=%s --field-selector=metadata.namespace=%s",
			plan.Namespace, managedByLabelKey, managedByLabelValue, plan.Namespace)
	}
	return fmt.Sprintf("kubectl apply -n %s -f - <<'EOF'\n%sEOF",
		plan.Namespace, plan.YAMLBefore)
}

func planContext(plan *rulePlan) map[string]any {
	context := map[string]any{
		"language":    string(plan.Language),
		"from_distro": plan.FromDistro,
		"to_distro":   plan.ToDistro,
	}
	switch plan.Op {
	case "enable_obi":
		context["ebpf_distro_name"] = plan.ToDistro
		context["prior_distro"] = plan.FromDistro
	case "disable_obi":
		context["removed_distro"] = plan.FromDistro
		context["restored_to"] = plan.ToDistro
	}
	return context
}

// resolveDistroForContainer reports which distro is active for a container,
// and where the choice came from. The lookup follows odigos's own priority:
//
//  1. Container-level override on the InstrumentationConfig spec.
//  2. The first matching InstrumentationRule whose OtelDistroNames contain
//     a distro registered for this language (or a wildcard distro).
//  3. The language default from the community defaulter.
//
// "Matches this language" means the distro entry's Language field is either
// equal to the container's detected language or the wildcard ("*"). A rule
// pinning [java-community] for a Go workload is ignored at this step
// (the instrumentor would not apply it either) so the agent does not report
// an obviously cross-language pin as "current distro".
func resolveDistroForContainer(
	config *odigosv1.InstrumentationConfig,
	rules []odigosv1.InstrumentationRule,
	defaults map[common.ProgrammingLanguage]string,
	containerName string,
	language common.ProgrammingLanguage,
	distroLanguageOf func(string) common.ProgrammingLanguage,
) (string, string) {
	for _, override := range config.Spec.ContainersOverrides {
		if override.ContainerName != containerName {
			continue
		}
		if override.OtelDistroName != nil && *override.OtelDistroName != "" {
			return *override.OtelDistroName, "container-override"
		}
	}
	for _, rule := range rules {
		if rule.Spec.OtelDistros == nil {
			continue
		}
		for _, name := range rule.Spec.OtelDistros.OtelDistroNames {
			distroLanguage := distroLanguageOf(name)
			if distroLanguage == language || distroLanguage == common.ProgrammingLanguageWildcard {
				return name, fmt.Sprintf("rule:%s", rule.Name)
			}
		}
	}
	if defaultName, ok := defaults[language]; ok {
		return defaultName, "default"
	}
	return "", "unknown"
}

func (m *instrumentationManager) distroLanguage(name string) common.ProgrammingLanguage {
	d := m.distros.GetDistroByName(name)
	if d == nil {
		return ""
	}
	return d.Language
}

func resolveLanguageDistro(
	config *odigosv1.InstrumentationConfig,
	rules []odigosv1.InstrumentationRule,
	defaults map[common.ProgrammingLanguage]string,
	language common.ProgrammingLanguage,
	distroLanguageOf func(string) common.ProgrammingLanguage,
) (string, string) {
	// Pick the first container matching the language.
	for _, runtimeDetails := range config.Status.RuntimeDetailsByContainer {
		if runtimeDetails.Language != language {
			continue
		}
		return resolveDistroForContainer(config, rules, defaults, runtimeDetails.ContainerName, language, distroLanguageOf)
	}
	if defaultName, ok := defaults[language]; ok {
		return defaultName, "default"
	}
	return "", "unknown"
}

func (m *instrumentationManager) ebpfDistroForLanguage(language common.ProgrammingLanguage) string {
	if name, ok := m.ebpfByLanguage[language]; ok {
		return name
	}
	if name, ok := m.ebpfByLanguage[common.ProgrammingLanguageWildcard]; ok {
		return name
	}
	if m.distros.GetDistroByName(obiUniversalDistroName) != nil {
		return obiUniversalDistroName
	}
	return ""
}

func (m *instrumentationManager) isEbpfDistro(name string) bool {
	if name == "" {
		return false
	}
	distroEntry := m.distros.GetDistroByName(name)
	if distroEntry == nil {
		return false
	}
	return distroEntry.IsEbpf
}

func distroSummary(d *distro.OtelDistro) map[string]any {
	runtimes := make([]map[string]string, 0, len(d.RuntimeEnvironments))
	for _, runtime := range d.RuntimeEnvironments {
		runtimes = append(runtimes, map[string]string{
			"name":               runtime.Name,
			"supported_versions": runtime.SupportedVersions,
		})
	}
	return map[string]any{
		"name":               d.Name,
		"display_name":       d.DisplayName,
		"language":           string(d.Language),
		"is_ebpf":            d.IsEbpf,
		"description":        d.Description,
		"runtimes":           runtimes,
		"requires_parameters": d.RequireParameters,
	}
}

func uniqueLanguages(details []odigosv1.RuntimeDetailsByContainer) []common.ProgrammingLanguage {
	seen := map[common.ProgrammingLanguage]struct{}{}
	out := make([]common.ProgrammingLanguage, 0)
	for _, runtimeDetails := range details {
		if runtimeDetails.Language == "" {
			continue
		}
		if _, exists := seen[runtimeDetails.Language]; exists {
			continue
		}
		seen[runtimeDetails.Language] = struct{}{}
		out = append(out, runtimeDetails.Language)
	}
	return out
}

func stringifyLanguages(languages []common.ProgrammingLanguage) []string {
	out := make([]string, 0, len(languages))
	for _, language := range languages {
		out = append(out, string(language))
	}
	return out
}

func stripRuleServerFields(rule *odigosv1.InstrumentationRule) *odigosv1.InstrumentationRule {
	if rule == nil {
		return nil
	}
	clone := rule.DeepCopy()
	clone.ManagedFields = nil
	clone.ResourceVersion = ""
	clone.SelfLink = ""
	clone.Generation = 0
	clone.CreationTimestamp = metav1.Time{}
	clone.UID = ""
	clone.Status = odigosv1.InstrumentationRuleStatus{}
	return clone
}

func buildSpecMergePatch(spec odigosv1.InstrumentationRuleSpec) ([]byte, error) {
	body := map[string]any{"spec": spec}
	return json.Marshal(body)
}

