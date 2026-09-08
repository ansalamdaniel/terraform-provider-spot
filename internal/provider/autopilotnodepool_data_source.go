package provider

import (
	"context"
	"fmt"

	ngpcv1 "github.com/RSS-Engineering/ngpc-cp/api/v1"
	"github.com/RSS-Engineering/ngpc-cp/pkg/ngpc"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	ktypes "k8s.io/apimachinery/pkg/types"

	"github.com/rackerlabs/terraform-provider-spot/internal/provider/datasource_autopilotnodepool"
)

var (
	_ datasource.DataSource              = (*autopilotnodepoolDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*autopilotnodepoolDataSource)(nil)
)

func NewAutopilotnodepoolDataSource() datasource.DataSource {
	return &autopilotnodepoolDataSource{}
}

type autopilotnodepoolDataSource struct {
	ngpcClient ngpc.Client
}

func (d *autopilotnodepoolDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_autopilotnodepool"
}

func (d *autopilotnodepoolDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_autopilotnodepool.AutopilotnodepoolDataSourceSchema(ctx)
}

func (d *autopilotnodepoolDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	d.ngpcClient = spotProviderData.ngpcClient
}

func (d *autopilotnodepoolDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data datasource_autopilotnodepool.AutopilotnodepoolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name, err := getNameFromNameOrId(data.Name.ValueString(), data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to get name from id", err.Error())
		return
	}
	namespace, err := getNamespaceFromEnv()
	if err != nil {
		resp.Diagnostics.AddError("Failed to get namespace from env", err.Error())
		return
	}

	tflog.Info(ctx, "Getting autopilotnodepool", map[string]any{"name": name, "namespace": namespace})
	autopilotnodepool := &ngpcv1.AutopilotNodePool{}
	if err := d.ngpcClient.Get(ctx, ktypes.NamespacedName{Name: name, Namespace: namespace}, autopilotnodepool); err != nil {
		resp.Diagnostics.AddError("Failed to get autopilotnodepool", err.Error())
		return
	}
	resp.Diagnostics.Append(setAutopilotNodePoolDataSourceState(ctx, autopilotnodepool, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func setAutopilotNodePoolDataSourceState(ctx context.Context, ap *ngpcv1.AutopilotNodePool, state *datasource_autopilotnodepool.AutopilotnodepoolModel) diag.Diagnostics {
	var diags diag.Diagnostics
	state.Id = types.StringValue(ap.ObjectMeta.Name)
	state.Name = types.StringValue(ap.ObjectMeta.Name)
	state.Region = types.StringValue(ap.Spec.Region)
	state.CloudspaceName = types.StringValue(ap.Spec.CloudSpace)
	state.BudgetPerHour = types.StringValue(ap.Spec.BudgetPerHour)
	state.MemoryPerVcpu = types.StringValue(ap.Spec.MemoryPerVCPU)
	state.AllocationStrategy = types.StringValue(ap.Spec.AllocationStrategy)

	vcpu, d := datasource_autopilotnodepool.NewVcpuValue(
		datasource_autopilotnodepool.VcpuValue{}.AttributeTypes(ctx),
		map[string]attr.Value{"total": types.Int64Value(int64(ap.Spec.VCPU.Total))},
	)
	diags.Append(d...)
	state.Vcpu = vcpu

	if ap.Spec.VCPUPerNode.Min == 0 && ap.Spec.VCPUPerNode.Max == 0 {
		state.VcpuPerNode = datasource_autopilotnodepool.NewVcpuPerNodeValueNull()
	} else {
		perNode, d := datasource_autopilotnodepool.NewVcpuPerNodeValue(
			datasource_autopilotnodepool.VcpuPerNodeValue{}.AttributeTypes(ctx),
			map[string]attr.Value{
				"min": int64OrNull(ap.Spec.VCPUPerNode.Min),
				"max": int64OrNull(ap.Spec.VCPUPerNode.Max),
			},
		)
		diags.Append(d...)
		state.VcpuPerNode = perNode
	}

	state.Phase = types.StringValue(ap.Status.Phase)
	state.TargetVcpus = types.Int64Value(int64(ap.Status.TargetVCPUs))
	state.ManagedVcpus = types.Int64Value(int64(ap.Status.ManagedVCPUs))
	state.ManagedMemoryGb = types.StringValue(ap.Status.ManagedMemoryGB)

	allocObjType := types.ObjectType{AttrTypes: datasource_autopilotnodepool.AllocationsValue{}.AttributeTypes(ctx)}
	if len(ap.Status.Allocations) > 0 {
		allocs := make([]attr.Value, 0, len(ap.Status.Allocations))
		for _, a := range ap.Status.Allocations {
			obj, d := types.ObjectValue(
				datasource_autopilotnodepool.AllocationsValue{}.AttributeTypes(ctx),
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
		state.Allocations = types.ListValueMust(allocObjType, []attr.Value{})
	}

	if ap.Spec.CustomLabels != nil {
		m, d := types.MapValueFrom(ctx, types.StringType, ap.Spec.CustomLabels)
		diags.Append(d...)
		state.Labels = m
	} else {
		state.Labels = types.MapNull(types.StringType)
	}
	if ap.Spec.CustomAnnotations != nil {
		m, d := types.MapValueFrom(ctx, types.StringType, ap.Spec.CustomAnnotations)
		diags.Append(d...)
		state.Annotations = m
	} else {
		state.Annotations = types.MapNull(types.StringType)
	}

	taintsObjType := types.ObjectType{AttrTypes: datasource_autopilotnodepool.TaintsValue{}.AttributeTypes(ctx)}
	if len(ap.Spec.CustomTaints) > 0 {
		taintsList := make([]attr.Value, 0, len(ap.Spec.CustomTaints))
		for _, taint := range ap.Spec.CustomTaints {
			obj, d := types.ObjectValue(
				datasource_autopilotnodepool.TaintsValue{}.AttributeTypes(ctx),
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
		state.Taints = types.ListValueMust(taintsObjType, []attr.Value{})
	}

	return diags
}
