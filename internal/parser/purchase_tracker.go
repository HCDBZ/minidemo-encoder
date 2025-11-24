package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	common "github.com/markus-wa/demoinfocs-golang/v2/pkg/demoinfocs/common"
)

// ItemAction 物品动作类型
type ItemAction string

const (
	ActionPurchase ItemAction = "purchased" // 购买
	ActionPickup   ItemAction = "picked_up" // 捡起
	ActionDrop     ItemAction = "dropped"   // 丢弃
)

// PurchaseRecord 记录单次购买/捡起
type PurchaseRecord struct {
	Time   float64    `json:"time"`   // 时间(秒)
	Item   string     `json:"item"`   // 物品名称
	Slot   string     `json:"slot"`   // 装备槽位
	Action ItemAction `json:"action"` // 动作类型:purchased/picked_up/dropped
}

type PlayerPurchaseData struct {
	Purchases             []PurchaseRecord `json:"purchases"`
	FinalInventory        []string         `json:"final_inventory"`
	FreezetimeEndGrenades []string         `json:"-"`
}

// RoundPurchaseData 回合购买数据
type RoundPurchaseData struct {
	T  map[string]*PlayerPurchaseData `json:"T"`  // T方玩家数据
	CT map[string]*PlayerPurchaseData `json:"CT"` // CT方玩家数据
}

// AllRoundsPurchaseData 所有回合的购买数据
type AllRoundsPurchaseData map[string]*RoundPurchaseData // key: "round1", "round2", ...

// WeaponTracker 用于跟踪武器归属,区分购买和捡起
type WeaponTracker struct {
	weaponOwners     map[int64]uint64 // weaponID -> steamID64
	weaponBought     map[int64]bool   // weaponID -> 是否是购买的(而非初始装备)
	weaponDropped    map[int64]bool   // weaponID -> 是否已被丢弃(在地上)
	weaponPrevOwners map[int64]uint64 // weaponID -> 上一个拥有者的steamID64
}

func NewWeaponTracker() *WeaponTracker {
	return &WeaponTracker{
		weaponOwners:     make(map[int64]uint64),
		weaponBought:     make(map[int64]bool),
		weaponDropped:    make(map[int64]bool),
		weaponPrevOwners: make(map[int64]uint64),
	}
}

// IsPurchase 判断是否为购买(新武器)
func (wt *WeaponTracker) IsPurchase(weapon *common.Equipment, playerSteamID uint64) bool {
	if weapon == nil {
		return false
	}

	weaponID := weapon.UniqueID()

	// 先检查是否是被丢弃的武器(如果是,应该判定为捡起而非购买)
	if isDropped, exists := wt.weaponDropped[weaponID]; exists && isDropped {
		// 这个武器被丢弃过,不是购买
		return false
	}

	// 如果武器不存在记录中,说明是新购买的
	_, exists := wt.weaponOwners[weaponID]
	if !exists {
		wt.weaponOwners[weaponID] = playerSteamID
		wt.weaponBought[weaponID] = true
		wt.weaponDropped[weaponID] = false
		wt.weaponPrevOwners[weaponID] = playerSteamID
		return true
	}

	// 如果武器已存在,不在这里判断(留给IsPickup)
	return false
}

// IsPickup 判断是否为捡起(武器已被丢弃且现在被其他人拿起)
func (wt *WeaponTracker) IsPickup(weapon *common.Equipment, playerSteamID uint64) bool {
	if weapon == nil {
		return false
	}

	weaponID := weapon.UniqueID()

	// 检查武器是否存在记录
	_, exists := wt.weaponPrevOwners[weaponID]
	if !exists {
		// 武器没有记录,不是捡起
		return false
	}

	// 检查武器是否被丢弃过
	isDropped, droppedExists := wt.weaponDropped[weaponID]
	if !droppedExists || !isDropped {
		// 武器没有被丢弃,不是捡起
		return false
	}

	// 检查前主人
	prevOwner := wt.weaponPrevOwners[weaponID]
	if prevOwner == playerSteamID {
		// 自己捡起自己丢的武器,不算
		return false
	}

	// 是捡起!更新状态
	wt.weaponOwners[weaponID] = playerSteamID
	wt.weaponDropped[weaponID] = false // 不再在地上
	wt.weaponPrevOwners[weaponID] = playerSteamID

	return true
}

// RegisterDrop 注册武器丢弃
func (wt *WeaponTracker) RegisterDrop(weapon *common.Equipment, playerSteamID uint64) {
	if weapon == nil {
		return
	}
	weaponID := weapon.UniqueID()

	// 标记武器已被丢弃(在地上)
	wt.weaponDropped[weaponID] = true
	// 保存上一个拥有者
	wt.weaponPrevOwners[weaponID] = playerSteamID
	// 移除当前所有者(因为武器在地上)
	delete(wt.weaponOwners, weaponID)
}

// 获取装备槽位
func getEquipmentSlot(eqType common.EquipmentType) string {
	switch eqType {
	case common.EqP2000, common.EqGlock, common.EqP250, common.EqDeagle,
		common.EqFiveSeven, common.EqTec9, common.EqCZ, common.EqUSP,
		common.EqRevolver, common.EqDualBerettas:
		return "pistol"
	case common.EqXM1014, common.EqMag7, common.EqSawedOff, common.EqNova:
		return "heavy"
	case common.EqMP7, common.EqMP9, common.EqMP5, common.EqUMP,
		common.EqP90, common.EqBizon, common.EqMac10:
		return "smg"
	case common.EqAK47, common.EqM4A4, common.EqM4A1, common.EqGalil,
		common.EqFamas, common.EqAUG, common.EqSG556:
		return "rifle"
	case common.EqAWP, common.EqScout, common.EqScar20, common.EqG3SG1:
		return "sniper"
	case common.EqM249, common.EqNegev:
		return "heavy"
	case common.EqFlash, common.EqSmoke, common.EqHE,
		common.EqMolotov, common.EqIncendiary, common.EqDecoy:
		return "grenade"
	case common.EqKevlar, common.EqHelmet, common.EqDefuseKit:
		return "gear"
	case common.EqKnife, common.EqWorld:
		return "knife"
	case common.EqZeus:
		return "zeus"
	default:
		return "unknown"
	}
}

// 获取装备名称(符合CS:GO命名规则)
func getEquipmentName(weapon *common.Equipment) string {
	if weapon == nil {
		return ""
	}

	// 使用 EquipmentType 进行映射
	switch weapon.Type {
	// 手枪
	case common.EqGlock:
		return "glock"
	case common.EqP2000:
		return "hkp2000"
	case common.EqUSP:
		return "usp_silencer"
	case common.EqP250:
		return "p250"
	case common.EqDeagle:
		return "deagle"
	case common.EqFiveSeven:
		return "fiveseven"
	case common.EqTec9:
		return "tec9"
	case common.EqCZ:
		return "cz75a"
	case common.EqRevolver:
		return "revolver"
	case common.EqDualBerettas:
		return "elite"

	// 冲锋枪
	case common.EqMP7:
		return "mp7"
	case common.EqMP9:
		return "mp9"
	case common.EqMP5:
		return "mp5sd"
	case common.EqUMP:
		return "ump45"
	case common.EqP90:
		return "p90"
	case common.EqBizon:
		return "bizon"
	case common.EqMac10:
		return "mac10"

	// 步枪
	case common.EqAK47:
		return "ak47"
	case common.EqM4A4:
		return "m4a1"
	case common.EqM4A1:
		return "m4a1_silencer"
	case common.EqGalil:
		return "galilar"
	case common.EqFamas:
		return "famas"
	case common.EqAUG:
		return "aug"
	case common.EqSG556:
		return "sg556"

	// 狙击步枪
	case common.EqAWP:
		return "awp"
	case common.EqScout:
		return "ssg08"
	case common.EqScar20:
		return "scar20"
	case common.EqG3SG1:
		return "g3sg1"

	// 霰弹枪
	case common.EqXM1014:
		return "xm1014"
	case common.EqMag7:
		return "mag7"
	case common.EqSawedOff:
		return "sawedoff"
	case common.EqNova:
		return "nova"

	// 机枪
	case common.EqM249:
		return "m249"
	case common.EqNegev:
		return "negev"

	// 手雷
	case common.EqFlash:
		return "flashbang"
	case common.EqSmoke:
		return "smokegrenade"
	case common.EqHE:
		return "hegrenade"
	case common.EqMolotov:
		return "molotov"
	case common.EqIncendiary:
		return "incgrenade"
	case common.EqDecoy:
		return "decoy"

	// 装备
	case common.EqKevlar:
		return "vest"
	case common.EqHelmet:
		return "vesthelm"
	case common.EqDefuseKit:
		return "defuser"

	// Zeus
	case common.EqZeus:
		return "taser"

	// 炸弹
	case common.EqBomb:
		return "c4"

	// 刀
	case common.EqKnife:
		return "knife"

	default:
		// 如果没有匹配到,尝试从字符串转换
		weaponStr := weapon.String()
		return normalizeWeaponName(weaponStr)
	}
}

// 标准化武器名称(后备方案)
func normalizeWeaponName(name string) string {
	// 转换为小写
	name = strings.ToLower(name)

	// 移除特殊字符和空格
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")

	// 特殊映射
	replacements := map[string]string{
		"ak47":          "ak47",
		"m4a4":          "m4a1",
		"m4a1s":         "m4a1_silencer",
		"usps":          "usp_silencer",
		"glock18":       "glock",
		"deserteagle":   "deagle",
		"ssg08":         "ssg08",
		"kevlar":        "vest",
		"kevlar+helmet": "vesthelm",
		"defusekit":     "defuser",
		"flashbang":     "flashbang",
		"smokegrenade":  "smokegrenade",
		"hegrenade":     "hegrenade",
	}

	if normalized, ok := replacements[name]; ok {
		return normalized
	}

	return name
}

// 判断是否应该过滤的武器(初始武器 + 不需要记录购买的)
func shouldFilterWeapon(weaponName string) bool {
	// 初始武器和刀、C4不记录
	filtered := []string{
		"glock",        // T方初始手枪
		"hkp2000",      // CT方初始手枪
		"usp_silencer", // CT方初始手枪(可选)
		"knife",        // 刀
		"c4",           // 炸弹
	}
	for _, f := range filtered {
		if weaponName == f {
			return true
		}
	}
	return false
}

// 判断是否应该在 final_inventory 中过滤(只过滤刀和C4)
func shouldFilterFromInventory(weaponName string) bool {
	filtered := []string{"knife", "c4"}
	for _, f := range filtered {
		if weaponName == f {
			return true
		}
	}
	return false
}

// 保存购买数据到JSON文件 - 按回合顺序输出
func savePurchaseData(data AllRoundsPurchaseData) error {
	// 使用 outputBaseDir 变量(从 parser.go 传递)
	outputFile := filepath.Join(outputBaseDir, "purchases.json")

	// 确保输出目录存在
	os.MkdirAll(outputBaseDir, 0755)

	// 提取所有回合号并排序
	roundNums := []int{}
	for key := range data {
		var num int
		fmt.Sscanf(key, "round%d", &num)
		roundNums = append(roundNums, num)
	}
	sort.Ints(roundNums)

	// 使用标准 JSON 编码(更可靠)
	orderedData := make(map[string]*RoundPurchaseData)
	for _, num := range roundNums {
		key := fmt.Sprintf("round%d", num)
		orderedData[key] = data[key]
	}

	// 序列化整个数据
	jsonData, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	// 写入文件
	err = os.WriteFile(outputFile, jsonData, 0644)
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

// 获取玩家最终装备列表
func getFinalInventory(player *common.Player) []string {
	if player == nil {
		return []string{}
	}

	inventory := []string{}
	weaponsSeen := make(map[string]bool) // 用于去重

	// 遍历玩家的所有武器
	for _, weapon := range player.Weapons() {
		if weapon == nil {
			continue
		}

		weaponName := getEquipmentName(weapon)
		// 只过滤刀和C4,保留初始手枪
		if weaponName != "" && !shouldFilterFromInventory(weaponName) && !weaponsSeen[weaponName] {
			inventory = append(inventory, weaponName)
			weaponsSeen[weaponName] = true
		}
	}

	// 添加护甲信息
	if player.HasHelmet() {
		if !weaponsSeen["vesthelm"] {
			inventory = append(inventory, "vesthelm")
			weaponsSeen["vesthelm"] = true
		}
	} else if player.Armor() > 0 {
		if !weaponsSeen["vest"] {
			inventory = append(inventory, "vest")
			weaponsSeen["vest"] = true
		}
	}

	// 添加拆弹器(仅CT)
	if player.HasDefuseKit() {
		if !weaponsSeen["defuser"] {
			inventory = append(inventory, "defuser")
			weaponsSeen["defuser"] = true
		}
	}

	return inventory
}

// ============================================================================
// 金钱数据系统
// ============================================================================

// MoneyData 存储金钱数据
type MoneyData struct {
	RoundMoney map[string]map[string]map[string]int `json:"-"` // round -> team -> playerName -> money
}

// 全局金钱数据收集器
var allMoneyData = &MoneyData{
	RoundMoney: make(map[string]map[string]map[string]int),
}

// 装备价格表
var equipmentPrices = map[string]int{
	// 主武器
	"ak47":          2700,
	"m4a1":          3100,
	"m4a1_silencer": 2900,
	"awp":           4750,
	"famas":         2250,
	"galilar":       2000,
	"ssg08":         1700,
	"aug":           3300,
	"sg556":         3000,
	"scar20":        5000,
	"g3sg1":         5000,
	// 冲锋枪
	"mp9":   1250,
	"mac10": 1050,
	"ump45": 1200,
	"p90":   2350,
	"bizon": 1400,
	"mp7":   1500,
	"mp5sd": 1500,
	// 霰弹枪
	"nova":     1050,
	"xm1014":   2000,
	"mag7":     1300,
	"sawedoff": 1100,
	// 机枪
	"m249":  5200,
	"negev": 1700,
	// 副武器
	"deagle":       700,
	"p250":         300,
	"tec9":         500,
	"fiveseven":    500,
	"cz75a":        500,
	"elite":        300,
	"revolver":     600,
	"hkp2000":      200,
	"usp_silencer": 200,
	"glock":        200,
	// 手雷
	"smokegrenade": 300,
	"flashbang":    200,
	"hegrenade":    300,
	"molotov":      400,
	"incgrenade":   600,
	"decoy":        50,
	// 装备
	"vest":     650,
	"vesthelm": 1000,
	"defuser":  400,
	"taser":    200,
}

// 获取装备价格
func getEquipmentPrice(itemName string) int {
	if price, ok := equipmentPrices[itemName]; ok {
		return price
	}
	return 0
}

// 计算玩家最终装备的总价值
func calculateFinalInventoryCost(inventory []string) int {
	totalCost := 0
	for _, item := range inventory {
		totalCost += getEquipmentPrice(item)
	}
	return totalCost
}

// 记录玩家回合开始金钱(在 RoundStart 事件中调用)
func recordPlayerStartMoney(player *common.Player, roundNum int) {
	if player == nil {
		return
	}

	roundKey := fmt.Sprintf("round%d", roundNum)

	// 初始化回合数据
	if allMoneyData.RoundMoney[roundKey] == nil {
		allMoneyData.RoundMoney[roundKey] = make(map[string]map[string]int)
		allMoneyData.RoundMoney[roundKey]["T"] = make(map[string]int)
		allMoneyData.RoundMoney[roundKey]["CT"] = make(map[string]int)
	}

	// 获取玩家金钱
	money := player.Money()

	// 根据队伍添加金钱
	teamKey := ""
	if player.Team == common.TeamTerrorists {
		teamKey = "T"
	} else if player.Team == common.TeamCounterTerrorists {
		teamKey = "CT"
	} else {
		return
	}

	// 使用玩家名称作为key存储金钱
	allMoneyData.RoundMoney[roundKey][teamKey][player.Name] = money

	// 调试日志
	ilog.InfoLogger.Printf("  [记录金钱] %s - %s: $%d",
		teamKey, player.Name, money)
}

// 计算玩家实际花费的经济(包括购买、发枪、捡枪)
func calculatePlayerActualCost(purchases []PurchaseRecord) int {
	purchasedCost := 0 // 购买的物品总价
	droppedCost := 0   // 发出的物品总价
	pickedCost := 0    // 捡起的物品总价

	for _, record := range purchases {
		itemPrice := getEquipmentPrice(record.Item)

		switch record.Action {
		case ActionPurchase:
			purchasedCost += itemPrice
		case ActionDrop:
			droppedCost += itemPrice
		case ActionPickup:
			pickedCost += itemPrice
		}
	}

	// 实际消费 = 购买的 + 发出的 - 捡起的
	actualCost := purchasedCost + droppedCost - pickedCost

	return actualCost
}

// 改进的金钱调整函数
func adjustMoneyForFinalInventory(roundPurchases *RoundPurchaseData, roundNum int) {
	roundKey := fmt.Sprintf("round%d", roundNum)

	if allMoneyData.RoundMoney[roundKey] == nil {
		ilog.InfoLogger.Printf("  [金钱调整] 警告: round%d 没有金钱数据", roundNum)
		return
	}

	ilog.InfoLogger.Printf("  [金钱调整] 开始调整 round%d 的金钱数据", roundNum)

	// 处理 T 方
	if moneyMap, ok := allMoneyData.RoundMoney[roundKey]["T"]; ok {
		for playerName, originalMoney := range moneyMap {
			// 检查该玩家是否在购买数据中
			playerData, exists := roundPurchases.T[playerName]
			if !exists {
				ilog.InfoLogger.Printf("    [金钱检查] T - %s: $%d (无购买数据)",
					playerName, originalMoney)
				continue
			}

			// 计算实际花费(包括购买、发枪、捡枪)
			actualCost := calculatePlayerActualCost(playerData.Purchases)

			// 计算最终装备价值
			finalInventoryCost := calculateFinalInventoryCost(playerData.FinalInventory)

			// 检查最终装备中哪些物品没有在purchases中出现
			purchasedItems := make(map[string]bool)
			for _, record := range playerData.Purchases {
				if record.Action == ActionPurchase || record.Action == ActionPickup {
					purchasedItems[record.Item] = true
				}
			}

			// 最终装备中未被购买/捡起的物品需要加入成本
			totalRequired := actualCost
			for _, item := range playerData.FinalInventory {
				if !purchasedItems[item] {
					totalRequired += getEquipmentPrice(item)
				}
			}

			adjustedValue := originalMoney
			if originalMoney < totalRequired {
				adjustedValue = totalRequired
				allMoneyData.RoundMoney[roundKey]["T"][playerName] = adjustedValue
				ilog.InfoLogger.Printf("    [金钱调整] T - %s: $%d -> $%d", playerName, originalMoney, adjustedValue)
				ilog.InfoLogger.Printf("        购买成本: $%d, 发枪成本: $%d, 捡枪节省: $%d, 最终装备: $%d",
					getPurchaseCost(playerData.Purchases),
					getDropCost(playerData.Purchases),
					getPickupValue(playerData.Purchases),
					finalInventoryCost)
			} else {
				ilog.InfoLogger.Printf("    [金钱检查] T - %s: $%d (实际消费: $%d, 无需调整)",
					playerName, originalMoney, totalRequired)
			}
		}
	}

	// 处理 CT 方
	if moneyMap, ok := allMoneyData.RoundMoney[roundKey]["CT"]; ok {
		for playerName, originalMoney := range moneyMap {
			playerData, exists := roundPurchases.CT[playerName]
			if !exists {
				ilog.InfoLogger.Printf("    [金钱检查] CT - %s: $%d (无购买数据)",
					playerName, originalMoney)
				continue
			}

			actualCost := calculatePlayerActualCost(playerData.Purchases)
			finalInventoryCost := calculateFinalInventoryCost(playerData.FinalInventory)

			purchasedItems := make(map[string]bool)
			for _, record := range playerData.Purchases {
				if record.Action == ActionPurchase || record.Action == ActionPickup {
					purchasedItems[record.Item] = true
				}
			}

			totalRequired := actualCost
			for _, item := range playerData.FinalInventory {
				if !purchasedItems[item] {
					totalRequired += getEquipmentPrice(item)
				}
			}

			adjustedValue := originalMoney
			if originalMoney < totalRequired {
				adjustedValue = totalRequired
				allMoneyData.RoundMoney[roundKey]["CT"][playerName] = adjustedValue
				ilog.InfoLogger.Printf("    [金钱调整] CT - %s: $%d -> $%d", playerName, originalMoney, adjustedValue)
				ilog.InfoLogger.Printf("        购买成本: $%d, 发枪成本: $%d, 捡枪节省: $%d, 最终装备: $%d",
					getPurchaseCost(playerData.Purchases),
					getDropCost(playerData.Purchases),
					getPickupValue(playerData.Purchases),
					finalInventoryCost)
			} else {
				ilog.InfoLogger.Printf("    [金钱检查] CT - %s: $%d (实际消费: $%d, 无需调整)",
					playerName, originalMoney, totalRequired)
			}
		}
	}
}

// 辅助函数:获取购买成本
func getPurchaseCost(purchases []PurchaseRecord) int {
	cost := 0
	for _, record := range purchases {
		if record.Action == ActionPurchase {
			cost += getEquipmentPrice(record.Item)
		}
	}
	return cost
}

// 辅助函数:获取发枪成本
func getDropCost(purchases []PurchaseRecord) int {
	cost := 0
	for _, record := range purchases {
		if record.Action == ActionDrop {
			cost += getEquipmentPrice(record.Item)
		}
	}
	return cost
}

// 辅助函数:获取捡枪节省的价值
func getPickupValue(purchases []PurchaseRecord) int {
	value := 0
	for _, record := range purchases {
		if record.Action == ActionPickup {
			value += getEquipmentPrice(record.Item)
		}
	}
	return value
}

// 保存金钱数据到JSON文件
func saveMoneyData() error {
	outputFile := filepath.Join(outputBaseDir, "money.json")

	// 确保输出目录存在
	os.MkdirAll(outputBaseDir, 0755)

	// 提取所有回合号并排序
	roundNums := []int{}
	for key := range allMoneyData.RoundMoney {
		var num int
		fmt.Sscanf(key, "round%d", &num)
		roundNums = append(roundNums, num)
	}
	sort.Ints(roundNums)

	// 按顺序构建数据
	orderedData := make(map[string]map[string]map[string]int)
	for _, num := range roundNums {
		key := fmt.Sprintf("round%d", num)
		orderedData[key] = allMoneyData.RoundMoney[key]
	}

	// 序列化为JSON
	jsonData, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	// 写入文件
	err = os.WriteFile(outputFile, jsonData, 0644)
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	ilog.InfoLogger.Printf("✓ 金钱数据已保存到: %s", outputFile)
	return nil
}
