package collectors

// GPUSpec holds hardware specifications for a known GPU model
type GPUSpec struct {
	Cores    int    `json:"cores"`
	CoreType string `json:"core_type"`
	Bus      string `json:"bus"`
	Mem      string `json:"mem"`
	TDPMin   int    `json:"tdp_min"`
	TDPMax   int    `json:"tdp_max"`
	Arch     string `json:"arch"`
}

// gpuSpecsDB is the embedded GPU specifications database
// Migrated from /data/dashboard/lib/gpu_specs.json
var gpuSpecsDB = map[string]GPUSpec{
	// NVIDIA Ampere
	"NVIDIA GeForce RTX 3050":    {2560, "CUDA", "128-bit", "GDDR6", 75, 130, "Ampere"},
	"NVIDIA GeForce RTX 3060":    {3584, "CUDA", "192-bit", "GDDR6", 130, 170, "Ampere"},
	"NVIDIA GeForce RTX 3060 Ti": {4864, "CUDA", "256-bit", "GDDR6", 160, 200, "Ampere"},
	"NVIDIA GeForce RTX 3070":    {5888, "CUDA", "256-bit", "GDDR6", 180, 220, "Ampere"},
	"NVIDIA GeForce RTX 3070 Ti": {6144, "CUDA", "256-bit", "GDDR6X", 220, 290, "Ampere"},
	"NVIDIA GeForce RTX 3080":    {8704, "CUDA", "320-bit", "GDDR6X", 200, 320, "Ampere"},
	"NVIDIA GeForce RTX 3080 Ti": {10240, "CUDA", "384-bit", "GDDR6X", 250, 350, "Ampere"},
	"NVIDIA GeForce RTX 3090":    {10496, "CUDA", "384-bit", "GDDR6X", 250, 350, "Ampere"},
	"NVIDIA GeForce RTX 3090 Ti": {10752, "CUDA", "384-bit", "GDDR6X", 350, 450, "Ampere"},
	// NVIDIA Ada Lovelace
	"NVIDIA GeForce RTX 4060":          {3072, "CUDA", "128-bit", "GDDR6", 75, 115, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4060 Ti":       {4352, "CUDA", "128-bit", "GDDR6", 130, 165, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4070":          {5888, "CUDA", "192-bit", "GDDR6X", 170, 200, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4070 Ti":       {7680, "CUDA", "192-bit", "GDDR6X", 220, 285, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4070 Ti SUPER": {8448, "CUDA", "256-bit", "GDDR6X", 250, 285, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4080":          {9728, "CUDA", "256-bit", "GDDR6X", 250, 320, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4080 SUPER":    {10240, "CUDA", "256-bit", "GDDR6X", 280, 320, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4090":          {16384, "CUDA", "384-bit", "GDDR6X", 350, 450, "Ada Lovelace"},
	"NVIDIA GeForce RTX 4090 D":        {14592, "CUDA", "384-bit", "GDDR6X", 350, 425, "Ada Lovelace"},
	// NVIDIA Blackwell
	"NVIDIA GeForce RTX 5060":    {3840, "CUDA", "128-bit", "GDDR7", 90, 115, "Blackwell"},
	"NVIDIA GeForce RTX 5060 Ti": {4608, "CUDA", "128-bit", "GDDR7", 130, 180, "Blackwell"},
	"NVIDIA GeForce RTX 5070":    {6144, "CUDA", "192-bit", "GDDR7", 180, 250, "Blackwell"},
	"NVIDIA GeForce RTX 5070 Ti": {8960, "CUDA", "256-bit", "GDDR7", 250, 300, "Blackwell"},
	"NVIDIA GeForce RTX 5080":    {10752, "CUDA", "256-bit", "GDDR7", 300, 360, "Blackwell"},
	"NVIDIA GeForce RTX 5090":    {21760, "CUDA", "512-bit", "GDDR7", 450, 575, "Blackwell"},
	// NVIDIA Data Center
	"NVIDIA Tesla A100":     {6912, "CUDA", "5120-bit", "HBM2e", 250, 400, "Ampere"},
	"NVIDIA A100-SXM4-40GB": {6912, "CUDA", "5120-bit", "HBM2e", 250, 400, "Ampere"},
	"NVIDIA A100-SXM4-80GB": {6912, "CUDA", "5120-bit", "HBM2e", 250, 400, "Ampere"},
	"NVIDIA H100":           {16896, "CUDA", "5120-bit", "HBM3", 350, 700, "Hopper"},
	"NVIDIA L40S":           {18176, "CUDA", "384-bit", "GDDR6", 250, 350, "Ada Lovelace"},
	// AMD RDNA 3
	"AMD Radeon RX 7600":     {2048, "Stream", "128-bit", "GDDR6", 100, 165, "RDNA 3"},
	"AMD Radeon RX 7700 XT":  {3456, "Stream", "192-bit", "GDDR6", 150, 245, "RDNA 3"},
	"AMD Radeon RX 7800 XT":  {3840, "Stream", "256-bit", "GDDR6", 190, 263, "RDNA 3"},
	"AMD Radeon RX 7900 GRE": {5120, "Stream", "256-bit", "GDDR6", 200, 260, "RDNA 3"},
	"AMD Radeon RX 7900 XT":  {5376, "Stream", "320-bit", "GDDR6", 250, 315, "RDNA 3"},
	"AMD Radeon RX 7900 XTX": {6144, "Stream", "384-bit", "GDDR6", 250, 355, "RDNA 3"},
	// AMD RDNA 4
	"AMD Radeon RX 9060 XT":  {2048, "Stream", "128-bit", "GDDR6", 90, 150, "RDNA 4"},
	"AMD Radeon RX 9070":     {3584, "Stream", "256-bit", "GDDR6", 150, 220, "RDNA 4"},
	"AMD Radeon RX 9070 XT":  {4096, "Stream", "256-bit", "GDDR6", 200, 300, "RDNA 4"},
	"AMD Radeon RX 9070 XTX": {4608, "Stream", "256-bit", "GDDR6", 250, 315, "RDNA 4"},
	// AMD CDNA
	"AMD Instinct MI210":  {6656, "Stream", "4096-bit", "HBM2e", 300, 500, "CDNA 2"},
	"AMD Instinct MI300X": {19456, "Stream", "8192-bit", "HBM3", 500, 750, "CDNA 3"},
	// Intel Arc
	"Intel Arc A770": {4096, "Xe", "256-bit", "GDDR6", 150, 225, "Xe HPG"},
	"Intel Arc A750": {3584, "Xe", "256-bit", "GDDR6", 150, 200, "Xe HPG"},
	"Intel Arc A580": {3072, "Xe", "192-bit", "GDDR6", 120, 185, "Xe HPG"},
	"Intel Arc B580": {2560, "Xe", "192-bit", "GDDR6", 130, 190, "Xe2"},
	"Intel Arc B570": {2304, "Xe", "160-bit", "GDDR6", 120, 150, "Xe2"},
}

// VendorInfo holds display metadata for a GPU vendor
type VendorInfo struct {
	Brand   string `json:"brand"`
	Color   string `json:"color"`
	Encoder string `json:"encoder"`
	Decoder string `json:"decoder"`
}

var vendorDB = map[string]VendorInfo{
	"nvidia":  {"NVIDIA", "#76b900", "NVENC", "NVDEC"},
	"amd":     {"AMD", "#ed1c24", "VCN", "VCN"},
	"intel":   {"Intel", "#0071c5", "QuickSync", "QuickSync"},
	"unknown": {"Unknown", "#8b949e", "N/A", "N/A"},
}

func lookupGPUSpec(name string) *GPUSpec {
	if name == "" {
		return nil
	}
	if spec, ok := gpuSpecsDB[name]; ok {
		return &spec
	}
	return nil
}

func getVendorInfo(vendor string) VendorInfo {
	if info, ok := vendorDB[vendor]; ok {
		return info
	}
	return vendorDB["unknown"]
}
