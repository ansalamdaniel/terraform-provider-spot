# Creates an autopilot node pool that fills 12 vCPUs of 4 GiB/vCPU capacity in
# us-central-dfw-1, never bidding more than 12.00 USD/hour. Spot picks the
# cheapest qualifying server classes and packs them to exactly the target.
resource "spot_autopilotnodepool" "example" {
  cloudspace_name = "example"
  region          = "us-central-dfw-1"
  vcpu = {
    total = 12
  }
  memory_per_vcpu = "4"
  budget_per_hour = "12.00"

  # Optional: bound the size of each individual node.
  # vcpu_per_node = {
  #   min = 4
  #   max = 8
  # }
}
