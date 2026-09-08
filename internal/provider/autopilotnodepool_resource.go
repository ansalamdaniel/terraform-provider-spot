package provider

import (
	"context"
	"fmt"
	"time"

	ngpcv1 "github.com/RSS-Engineering/ngpc-cp/api/v1"
	"github.com/RSS-Engineering/ngpc-cp/pkg/ngpc"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"

	"github.com/rackerlabs/terraform-provider-spot/internal/provider/resource_autopilotnodepool"
)

var (
	_ resource.Resource                = (*autopilotnodepoolResource)(nil)
	_ resource.ResourceWithConfigure   = (*autopilotnodepoolResource)(nil)
	_ resource.ResourceWithImportState = (*autopilotnodepoolResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*autopilotnodepoolResource)(nil)
)

func NewAutopilotnodepoolResource() resource.Resource {
	return &autopilotnodepoolResource{}
}

type autopilotnodepoolResource struct {
	ngpcClient ngpc.Client
}

func (r *autopilotnodepoolResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_autopilotnodepool"
}

func (r *autopilotnodepoolResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_autopilotnodepool.AutopilotnodepoolResourceSchema(ctx)
}

func (r *autopilotnodepoolResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	spotProviderData, ok := req.ProviderData.(*SpotProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *SpotProviderData, got: %T.", req.ProviderData),
		)
		return
	}
	r.ngpcClient = spotProviderData.ngpcClient
}

// customMetadataFromModel extracts labels/annotations/taints from the TF model into CRD types.
func customMetadataFromModel(ctx context.Context, labelsAttr, annotationsAttr types.Map, taintsAttr types.List) (map[string]string, map[string]string, []corev1.Taint, diag.Diagnostics) {
	var diags diag.Diagnostics
	var labels, annotations map[string]string
	var taints []corev1.Taint

	if !labelsAttr.IsNull() {
		labels = make(map[string]string)
		diags.Append(labelsAttr.ElementsAs(ctx, &labels, false)...)
	}
	if !annotationsAttr.IsNull() {
		annotations = make(map[string]string)
		diags.Append(annotationsAttr.ElementsAs(ctx, &annotations, false)...)
	}
	if !taintsAttr.IsNull() {
		var taintsList []resource_autopilotnodepool.TaintsValue
		diags.Append(taintsAttr.ElementsAs(ctx, &taintsList, false)...)
		taints = make([]corev1.Taint, 0, len(taintsList))
		for _, taint := range taintsList {
			taints = append(taints, corev1.Taint{
				Key:    taint.Key.ValueString(),
				Value:  taint.Value.ValueString(),
				Effect: corev1.TaintEffect(taint.Effect.ValueString()),
			})
		}
	}
	return labels, annotations, taints, diags
}

// vcpuPerNodeFromModel maps the optional vcpu_per_node block to the CRD range (zero value when unset).
func vcpuPerNodeFromModel(v resource_autopilotnodepool.VcpuPerNodeValue) ngpcv1.VCPUPerNodeRange {
	if v.IsNull() || v.IsUnknown() {
		return ngpcv1.VCPUPerNodeRange{}
	}
	return ngpcv1.VCPUPerNodeRange{
		Min: int(v.Min.ValueInt64()),
		Max: int(v.Max.ValueInt64()),
	}
}

func (r *autopilotnodepoolResource) buildSpec(ctx context.Context, m *resource_autopilotnodepool.AutopilotnodepoolModel) (ngpcv1.AutopilotNodePoolSpec, diag.Diagnostics) {
	labels, annotations, taints, diags := customMetadataFromModel(ctx, m.Labels, m.Annotations, m.Taints)
	return ngpcv1.AutopilotNodePoolSpec{
		Region:             m.Region.ValueString(),
		CloudSpace:         m.CloudspaceName.ValueString(),
		VCPU:               ngpcv1.VCPUTarget{Total: int(m.Vcpu.Total.ValueInt64())},
		VCPUPerNode:        vcpuPerNodeFromModel(m.VcpuPerNode),
		MemoryPerVCPU:      m.MemoryPerVcpu.ValueString(),
		BudgetPerHour:      m.BudgetPerHour.ValueString(),
		AllocationStrategy: m.AllocationStrategy.ValueString(),
		CustomLabels:       labels,
		CustomAnnotations:  annotations,
		CustomTaints:       taints,
	}, diags
}

func (r *autopilotnodepoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data resource_autopilotnodepool.AutopilotnodepoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := generateRandomUUID()
	if err != nil {
		resp.Diagnostics.AddError("Failed to generate random UUID", err.Error())
		return
	}
	namespace, err := getNamespaceFromEnv()
	if err != nil {
		resp.Diagnostics.AddError("Failed to get namespace", err.Error())
		return
	}

	spec, diags := r.buildSpec(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	autopilotNodePool := &ngpcv1.AutopilotNodePool{
		TypeMeta: metav1.TypeMeta{Kind: "AutopilotNodePool", APIVersion: "ngpc.rxt.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			// Tag the pool with its cloudspace so the UI (and label selectors) can group it under the
			// cloudspace, matching how existing pools are labelled.
			Labels: map[string]string{ngpcv1.LabelCloudSpace: data.CloudspaceName.ValueString()},
		},
		Spec: spec,
	}

	tflog.Debug(ctx, "Creating autopilotnodepool", map[string]any{"name": name, "namespace": namespace})
	if err := r.ngpcClient.Create(ctx, autopilotNodePool); err != nil {
		resp.Diagnostics.AddError("Failed to create autopilotnodepool", err.Error())
		return
	}
	tflog.Debug(ctx, "Created autopilotnodepool", map[string]any{"name": autopilotNodePool.ObjectMeta.Name})

	resp.Diagnostics.Append(setAutopilotNodePoolState(ctx, autopilotNodePool, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.LastUpdated = types.StringValue(time.Now().Format(time.RFC3339))
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, keyResourceVersion, []byte(autopilotNodePool.ObjectMeta.ResourceVersion))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *autopilotnodepoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data resource_autopilotnodepool.AutopilotnodepoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	namespace, err := getNamespaceFromEnv()
	if err != nil {
		resp.Diagnostics.AddError("Failed to get namespace", err.Error())
		return
	}

	tflog.Info(ctx, "Getting autopilotnodepool", map[string]any{"name": name, "namespace": namespace})
	autopilotnodepool := &ngpcv1.AutopilotNodePool{}
	if err := r.ngpcClient.Get(ctx, ktypes.NamespacedName{Name: name, Namespace: namespace}, autopilotnodepool); err != nil {
		resp.Diagnostics.AddError("Failed to get autopilotnodepool", err.Error())
		return
	}
	resp.Diagnostics.Append(setAutopilotNodePoolState(ctx, autopilotnodepool, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.LastUpdated = types.StringNull()
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, keyResourceVersion, []byte(autopilotnodepool.ObjectMeta.ResourceVersion))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *autopilotnodepoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state resource_autopilotnodepool.AutopilotnodepoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	namespace, err := getNamespaceFromEnv()
	if err != nil {
		resp.Diagnostics.AddError("Failed to get namespace", err.Error())
		return
	}

	// Get the latest version for Kubernetes optimistic concurrency (another controller may have
	// modified the resource between our read and update).
	latest := &ngpcv1.AutopilotNodePool{}
	if err := r.ngpcClient.Get(ctx, ktypes.NamespacedName{Name: name, Namespace: namespace}, latest); err != nil {
		resp.Diagnostics.AddError("Failed to get latest version of autopilotnodepool", err.Error())
		return
	}

	spec, diags := r.buildSpec(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	autopilotnodepool := &ngpcv1.AutopilotNodePool{
		TypeMeta: metav1.TypeMeta{Kind: "AutopilotNodePool", APIVersion: "ngpc.rxt.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: latest.ResourceVersion,
			Labels:          map[string]string{ngpcv1.LabelCloudSpace: plan.CloudspaceName.ValueString()},
		},
		Spec: spec,
	}
	tflog.Debug(ctx, "Updating autopilotnodepool", map[string]any{"name": name})
	if err := r.ngpcClient.Update(ctx, autopilotnodepool); err != nil {
		resp.Diagnostics.AddError("Failed to update autopilotnodepool", err.Error())
		return
	}
	resp.Diagnostics.Append(setAutopilotNodePoolState(ctx, autopilotnodepool, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, keyResourceVersion, []byte(autopilotnodepool.ObjectMeta.ResourceVersion))...)
	state.LastUpdated = types.StringValue(time.Now().Format(time.RFC3339))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *autopilotnodepoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data resource_autopilotnodepool.AutopilotnodepoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()
	namespace, err := getNamespaceFromEnv()
	if err != nil {
		resp.Diagnostics.AddError("Failed to get namespace", err.Error())
		return
	}
	tflog.Info(ctx, "Deleting autopilotnodepool", map[string]any{"name": name, "namespace": namespace})
	err = r.ngpcClient.Delete(ctx, &ngpcv1.AutopilotNodePool{
		TypeMeta:   metav1.TypeMeta{Kind: "AutopilotNodePool", APIVersion: "ngpc.rxt.io/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete autopilotnodepool", err.Error())
		return
	}
	tflog.Info(ctx, "Deleted autopilotnodepool", map[string]any{"name": name, "namespace": namespace})
}

func (r *autopilotnodepoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// ModifyPlan validates the region against the live catalogue (autopilot selects its own server
// classes, so there is no server_class to validate — only the region).
func (r *autopilotnodepoolResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy plan
	}
	var regionVal types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("region"), &regionVal)...)
	if regionVal.IsNull() || regionVal.IsUnknown() {
		return
	}
	regions, err := listRegions(ctx, r.ngpcClient)
	if err != nil {
		resp.Diagnostics.AddWarning("Failed to list regions", err.Error())
		return
	}
	for _, region := range regions {
		if region.Name == regionVal.ValueString() {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(path.Root("region"), "Invalid value",
		"The valid values should be read from the regions data source.")
}

func setAutopilotNodePoolState(ctx context.Context, ap *ngpcv1.AutopilotNodePool, state *resource_autopilotnodepool.AutopilotnodepoolModel) diag.Diagnostics {
	var diags diag.Diagnostics
	state.Name = types.StringValue(ap.ObjectMeta.Name)
	state.Id = types.StringValue(ap.ObjectMeta.Name)
	state.Region = types.StringValue(ap.Spec.Region)
	state.CloudspaceName = types.StringValue(ap.Spec.CloudSpace)
	state.BudgetPerHour = types.StringValue(ap.Spec.BudgetPerHour)
	state.MemoryPerVcpu = types.StringValue(ap.Spec.MemoryPerVCPU)
	state.AllocationStrategy = types.StringValue(ap.Spec.AllocationStrategy)

	// vcpu (required nested)
	vcpu, d := resource_autopilotnodepool.NewVcpuValue(
		resource_autopilotnodepool.VcpuValue{}.AttributeTypes(ctx),
		map[string]attr.Value{"total": types.Int64Value(int64(ap.Spec.VCPU.Total))},
	)
	diags.Append(d...)
	state.Vcpu = vcpu

	// vcpu_per_node (optional nested) — null when the CRD carries no bounds
	if ap.Spec.VCPUPerNode.Min == 0 && ap.Spec.VCPUPerNode.Max == 0 {
		state.VcpuPerNode = resource_autopilotnodepool.NewVcpuPerNodeValueNull()
	} else {
		perNode, d := resource_autopilotnodepool.NewVcpuPerNodeValue(
			resource_autopilotnodepool.VcpuPerNodeValue{}.AttributeTypes(ctx),
			map[string]attr.Value{
				"min": int64OrNull(ap.Spec.VCPUPerNode.Min),
				"max": int64OrNull(ap.Spec.VCPUPerNode.Max),
			},
		)
		diags.Append(d...)
		state.VcpuPerNode = perNode
	}

	// status scalars
	state.Phase = types.StringValue(ap.Status.Phase)
	state.TargetVcpus = types.Int64Value(int64(ap.Status.TargetVCPUs))
	state.ManagedVcpus = types.Int64Value(int64(ap.Status.ManagedVCPUs))
	state.ManagedMemoryGb = types.StringValue(ap.Status.ManagedMemoryGB)

	// allocations (computed status list)
	allocObjType := types.ObjectType{AttrTypes: resource_autopilotnodepool.AllocationsValue{}.AttributeTypes(ctx)}
	if len(ap.Status.Allocations) > 0 {
		allocs := make([]attr.Value, 0, len(ap.Status.Allocations))
		for _, a := range ap.Status.Allocations {
			obj, d := types.ObjectValue(
				resource_autopilotnodepool.AllocationsValue{}.AttributeTypes(ctx),
				map[string]attr.Value{
					"server_class":          types.StringValue(a.ServerClass),
					"spot_node_pool":        types.StringValue(a.SpotNodePool),
					"market_price_per_hour": types.StringValue(a.MarketPricePerHour),
					"bid_price_per_hour":    types.StringValue(a.BidPricePerHour),
					"vcpu_per_node":         types.Int64Value(int64(a.VCPUPerNode)),
					"memory_gb_per_node":    types.StringValue(a.MemoryGBPerNode),
					"desired_nodes":         types.Int64Value(int64(a.DesiredNodes)),
					"won_nodes":             types.Int64Value(int64(a.WonNodes)),
				},
			)
			diags.Append(d...)
			allocs = append(allocs, obj)
		}
		state.Allocations = types.ListValueMust(allocObjType, allocs)
	} else {
		state.Allocations = types.ListNull(allocObjType)
	}

	// labels / annotations
	if len(ap.Spec.CustomLabels) > 0 {
		m, d := types.MapValueFrom(ctx, types.StringType, ap.Spec.CustomLabels)
		diags.Append(d...)
		state.Labels = m
	} else {
		state.Labels = types.MapNull(types.StringType)
	}
	if len(ap.Spec.CustomAnnotations) > 0 {
		m, d := types.MapValueFrom(ctx, types.StringType, ap.Spec.CustomAnnotations)
		diags.Append(d...)
		state.Annotations = m
	} else {
		state.Annotations = types.MapNull(types.StringType)
	}

	// taints
	taintsObjType := types.ObjectType{AttrTypes: resource_autopilotnodepool.TaintsValue{}.AttributeTypes(ctx)}
	if len(ap.Spec.CustomTaints) > 0 {
		taintsList := make([]attr.Value, 0, len(ap.Spec.CustomTaints))
		for _, taint := range ap.Spec.CustomTaints {
			obj, d := types.ObjectValue(
				resource_autopilotnodepool.TaintsValue{}.AttributeTypes(ctx),
				map[string]attr.Value{
					"effect": types.StringValue(string(taint.Effect)),
					"key":    types.StringValue(taint.Key),
					"value":  types.StringValue(taint.Value),
				},
			)
			diags.Append(d...)
			taintsList = append(taintsList, obj)
		}
		state.Taints = types.ListValueMust(taintsObjType, taintsList)
	} else {
		state.Taints = types.ListNull(taintsObjType)
	}

	return diags
}

// int64OrNull maps a zero (unset) CRD int to a null attr value, else the value.
func int64OrNull(v int) attr.Value {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(int64(v))
}
