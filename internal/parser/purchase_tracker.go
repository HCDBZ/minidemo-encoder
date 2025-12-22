package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
)

type ItemAction string

const (
	ActionPurchase ItemAction = "purchased"
	ActionPickup   ItemAction = "picked_up"
	ActionDrop     ItemAction = "dropped"
	ActionBuyDrop  ItemAction = "buy_dropped"
)

type PurchaseRecord struct {
	Time   float64    `json:"time"`
	Item   string     `json:"item"`
	Slot   string     `json:"slot"`
	Action ItemAction `json:"action"`
}

type PlayerPurchaseData struct {
	InitialInventory      []string         `json:"initial_inventory"`
	Purchases             []PurchaseRecord `json:"purchases"`
	FreezetimeEndGrenades []string         `json:"-"`
}

type RoundPurchaseData struct {
	T  map[string]*PlayerPurchaseData `json:"T"`
	CT map[string]*PlayerPurchaseData `json:"CT"`
}

type AllRoundsPurchaseData map[string]*RoundPurchaseData

// 改进的WeaponTracker
type WeaponTracker struct {
	seenWeapons    map[int64]bool   // 记录见过的所有武器UniqueID
	droppedWeapons map[int64]uint64 // 记录被丢弃武器及其前任拥有者
}

func NewWeaponTracker() *WeaponTracker {
	return &WeaponTracker{
		seenWeapons:    make(map[int64]bool, 100),
		droppedWeapons: make(map[int64]uint64, 50),
	}
}

// IsNewWeapon 判断是否为新武器（从未见过的UniqueID）
func (wt *WeaponTracker) IsNewWeapon(weapon *common.Equipment) bool {
	if weapon == nil {
		return false
	}
	weaponID := weapon.UniqueID()

	// 检查是否见过这个武器
	if wt.seenWeapons[weaponID] {
		return false
	}

	// 标记为已见过
	wt.seenWeapons[weaponID] = true
	return true
}

// RegisterDrop 记录武器被丢弃
func (wt *WeaponTracker) RegisterDrop(weapon *common.Equipment, playerSteamID uint64) {
	if weapon == nil {
		return
	}
	weaponID := weapon.UniqueID()
	wt.droppedWeapons[weaponID] = playerSteamID
}

// IsPickupFromGround 判断是否为捡起地上的武器
func (wt *WeaponTracker) IsPickupFromGround(weapon *common.Equipment, playerSteamID uint64) bool {
	if weapon == nil {
		return false
	}
	weaponID := weapon.UniqueID()

	// 必须在dropped列表中
	prevOwner, isDropped := wt.droppedWeapons[weaponID]
	if !isDropped {
		return false
	}

	// 不能是捡自己丢的
	if prevOwner == playerSteamID {
		return false
	}

	// 捡起后从dropped列表移除
	delete(wt.droppedWeapons, weaponID)
	return true
}

// 判断是否为手榴弹
func isGrenadeType(eqType common.EquipmentType) bool {
	switch eqType {
	case common.EqFlash, common.EqSmoke, common.EqHE,
		common.EqMolotov, common.EqIncendiary, common.EqDecoy:
		return true
	}
	return false
}

// 计算玩家持有的某类手榴弹数量
func countGrenadeType(player *common.Player, eqType common.EquipmentType) int {
	if player == nil {
		return 0
	}
	count := 0
	for _, weapon := range player.Weapons() {
		if weapon != nil && weapon.Type == eqType {
			count++
		}
	}
	return count
}

type MoneyData struct {
	RoundMoney map[string]map[string]map[string]int `json:"-"`
}

var allMoneyData = &MoneyData{
	RoundMoney: make(map[string]map[string]map[string]int, 30),
}

var equipmentPrices = map[string]int{
	"ak47": 2700, "m4a1": 3100, "m4a1_silencer": 2900, "awp": 4750,
	"famas": 2250, "galilar": 2000, "ssg08": 1700, "aug": 3300,
	"sg556": 3000, "scar20": 5000, "g3sg1": 5000,
	"mp9": 1250, "mac10": 1050, "ump45": 1200, "p90": 2350,
	"bizon": 1400, "mp7": 1500, "mp5sd": 1500,
	"nova": 1050, "xm1014": 2000, "mag7": 1300, "sawedoff": 1100,
	"m249": 5200, "negev": 1700,
	"deagle": 700, "p250": 300, "tec9": 500, "fn57": 500,
	"cz75a": 500, "elite": 300, "revolver": 600, "hkp2000": 200,
	"usp_silencer": 200, "glock": 200,
	"smokegrenade": 300, "flashbang": 200, "hegrenade": 300,
	"molotov": 400, "incgrenade": 600, "decoy": 50,
	"vest": 650, "vesthelm": 1000, "defuser": 400, "taser": 200,
}

func recordPlayerStartMoney(player *common.Player, roundNum int) {
	if player == nil {
		return
	}

	roundKey := fmt.Sprintf("round%d", roundNum)

	if allMoneyData.RoundMoney[roundKey] == nil {
		allMoneyData.RoundMoney[roundKey] = map[string]map[string]int{
			"T":  make(map[string]int, 5),
			"CT": make(map[string]int, 5),
		}
	}

	var teamKey string
	switch player.Team {
	case common.TeamTerrorists:
		teamKey = "T"
	case common.TeamCounterTerrorists:
		teamKey = "CT"
	default:
		return
	}

	allMoneyData.RoundMoney[roundKey][teamKey][player.Name] = player.Money()
}

func adjustMoneyForInitialInventory(roundPurchases *RoundPurchaseData, roundNum int) {
	roundKey := fmt.Sprintf("round%d", roundNum)

	if allMoneyData.RoundMoney[roundKey] == nil {
		return
	}

	for _, teamKey := range []string{"T", "CT"} {
		moneyMap, ok := allMoneyData.RoundMoney[roundKey][teamKey]
		if !ok {
			continue
		}

		var teamData map[string]*PlayerPurchaseData
		if teamKey == "T" {
			teamData = roundPurchases.T
		} else {
			teamData = roundPurchases.CT
		}

		for playerName, originalMoney := range moneyMap {
			playerData, exists := teamData[playerName]
			if !exists {
				continue
			}

			initialCost := calculateInventoryCostWithFilter(playerData.InitialInventory)
			purchaseCost := getPurchaseCost(playerData.Purchases)
			totalRequired := initialCost + purchaseCost

			if originalMoney < totalRequired {
				allMoneyData.RoundMoney[roundKey][teamKey][playerName] = totalRequired
			}
		}
	}
}

func getPurchaseCost(purchases []PurchaseRecord) int {
	cost := 0
	for _, record := range purchases {
		if record.Action == ActionPurchase && !shouldFilterWeapon(record.Item) {
			cost += getEquipmentPrice(record.Item)
		}
	}
	return cost
}

func getEquipmentPrice(itemName string) int {
	if price, ok := equipmentPrices[itemName]; ok {
		return price
	}
	return 0
}

func calculateInventoryCostWithFilter(inventory []string) int {
	totalCost := 0
	for _, item := range inventory {
		if !shouldFilterWeapon(item) {
			totalCost += getEquipmentPrice(item)
		}
	}
	return totalCost
}

func saveMoneyData() error {
	outputFile := filepath.Join(outputBaseDir, "money.json")
	os.MkdirAll(outputBaseDir, 0755)

	roundNums := make([]int, 0, len(allMoneyData.RoundMoney))
	for key := range allMoneyData.RoundMoney {
		var num int
		fmt.Sscanf(key, "round%d", &num)
		roundNums = append(roundNums, num)
	}
	sort.Ints(roundNums)

	orderedData := make(map[string]map[string]map[string]int, len(roundNums))
	for _, num := range roundNums {
		key := fmt.Sprintf("round%d", num)
		orderedData[key] = allMoneyData.RoundMoney[key]
	}

	jsonData, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	if err := os.WriteFile(outputFile, jsonData, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	ilog.InfoLogger.Printf("金钱数据已保存到: %s", outputFile)
	return nil
}

func getEquipmentSlot(eqType common.EquipmentType) string {
	switch eqType {
	case common.EqP2000, common.EqGlock, common.EqP250, common.EqDeagle,
		common.EqFiveSeven, common.EqTec9, common.EqCZ, common.EqUSP,
		common.EqRevolver, common.EqDualBerettas:
		return "pistol"
	case common.EqMP7, common.EqMP9, common.EqMP5, common.EqUMP,
		common.EqP90, common.EqBizon, common.EqMac10:
		return "smg"
	case common.EqAK47, common.EqM4A4, common.EqM4A1, common.EqGalil,
		common.EqFamas, common.EqAUG, common.EqSG556:
		return "rifle"
	case common.EqAWP, common.EqScout, common.EqScar20, common.EqG3SG1:
		return "sniper"
	case common.EqXM1014, common.EqMag7, common.EqSawedOff, common.EqNova,
		common.EqM249, common.EqNegev:
		return "heavy"
	case common.EqFlash, common.EqSmoke, common.EqHE,
		common.EqMolotov, common.EqIncendiary, common.EqDecoy:
		return "grenade"
	case common.EqKevlar, common.EqHelmet, common.EqDefuseKit:
		return "gear"
	case common.EqZeus:
		return "zeus"
	case common.EqKnife, common.EqWorld:
		return "knife"
	default:
		return "unknown"
	}
}

func getEquipmentName(weapon *common.Equipment) string {
	if weapon == nil {
		return ""
	}

	nameMap := map[common.EquipmentType]string{
		common.EqGlock: "glock", common.EqP2000: "hkp2000", common.EqUSP: "usp_silencer",
		common.EqP250: "p250", common.EqDeagle: "deagle", common.EqFiveSeven: "fn57",
		common.EqTec9: "tec9", common.EqCZ: "cz75a", common.EqRevolver: "revolver",
		common.EqDualBerettas: "elite",
		common.EqMP7:          "mp7", common.EqMP9: "mp9", common.EqMP5: "mp5sd",
		common.EqUMP: "ump45", common.EqP90: "p90", common.EqBizon: "bizon",
		common.EqMac10: "mac10",
		common.EqAK47:  "ak47", common.EqM4A4: "m4a1", common.EqM4A1: "m4a1_silencer",
		common.EqGalil: "galilar", common.EqFamas: "famas", common.EqAUG: "aug",
		common.EqSG556: "sg556",
		common.EqAWP:   "awp", common.EqScout: "ssg08", common.EqScar20: "scar20",
		common.EqG3SG1:  "g3sg1",
		common.EqXM1014: "xm1014", common.EqMag7: "mag7", common.EqSawedOff: "sawedoff",
		common.EqNova: "nova",
		common.EqM249: "m249", common.EqNegev: "negev",
		common.EqFlash: "flashbang", common.EqSmoke: "smokegrenade", common.EqHE: "hegrenade",
		common.EqMolotov: "molotov", common.EqIncendiary: "incgrenade", common.EqDecoy: "decoy",
		common.EqKevlar: "vest", common.EqHelmet: "vesthelm", common.EqDefuseKit: "defuser",
		common.EqZeus: "taser", common.EqBomb: "c4", common.EqKnife: "knife",
	}

	if name, ok := nameMap[weapon.Type]; ok {
		return name
	}

	return normalizeWeaponName(weapon.String())
}

func normalizeWeaponName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")

	replacements := map[string]string{
		"ak47": "ak47", "m4a4": "m4a1", "m4a1s": "m4a1_silencer",
		"usps": "usp_silencer", "glock18": "glock", "deserteagle": "deagle",
		"ssg08": "ssg08", "kevlar": "vest", "kevlar+helmet": "vesthelm",
		"defusekit": "defuser", "flashbang": "flashbang",
		"smokegrenade": "smokegrenade", "hegrenade": "hegrenade",
	}

	if normalized, ok := replacements[name]; ok {
		return normalized
	}

	return name
}

func shouldFilterWeapon(weaponName string) bool {
	filtered := map[string]bool{
		"glock": true, "hkp2000": true, "usp_silencer": true,
		"knife": true, "c4": true,
	}
	return filtered[weaponName]
}

func shouldFilterFromInventory(weaponName string) bool {
	return weaponName == "knife" || weaponName == "c4"
}

func isPrimaryWeapon(weaponName string) bool {
	primaryWeapons := map[string]bool{
		"ak47": true, "m4a1": true, "m4a1_silencer": true, "galilar": true,
		"famas": true, "aug": true, "sg556": true,
		"awp": true, "ssg08": true, "scar20": true, "g3sg1": true,
		"mp9": true, "mac10": true, "mp7": true, "ump45": true,
		"p90": true, "bizon": true, "mp5sd": true,
		"nova": true, "xm1014": true, "mag7": true, "sawedoff": true,
		"m249": true, "negev": true,
	}
	return primaryWeapons[weaponName]
}

func getFullInventory(player *common.Player) []string {
	if player == nil {
		return []string{}
	}

	inventory := []string{}
	weaponsSeen := make(map[string]bool, 10)

	for _, weapon := range player.Weapons() {
		if weapon == nil {
			continue
		}

		weaponName := getEquipmentName(weapon)
		if weaponName != "" && !weaponsSeen[weaponName] {
			inventory = append(inventory, weaponName)
			weaponsSeen[weaponName] = true
		}
	}

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

	if player.HasDefuseKit() && !weaponsSeen["defuser"] {
		inventory = append(inventory, "defuser")
	}

	return inventory
}

func getFinalInventory(player *common.Player) []string {
	if player == nil {
		return []string{}
	}

	inventory := []string{}
	weaponsSeen := make(map[string]bool, 10)

	for _, weapon := range player.Weapons() {
		if weapon == nil {
			continue
		}

		weaponName := getEquipmentName(weapon)
		if weaponName != "" && !shouldFilterFromInventory(weaponName) && !weaponsSeen[weaponName] {
			inventory = append(inventory, weaponName)
			weaponsSeen[weaponName] = true
		}
	}

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

	if player.HasDefuseKit() && !weaponsSeen["defuser"] {
		inventory = append(inventory, "defuser")
	}

	return inventory
}

func optimizeInitialInventory(playerData *PlayerPurchaseData) []string {
	secondaryPistols := map[string]bool{
		"p250": true, "deagle": true, "fn57": true, "tec9": true,
		"cz75a": true, "revolver": true, "elite": true,
	}

	primaryWeaponCount := 0

	for _, item := range playerData.InitialInventory {
		if isPrimaryWeapon(item) {
			primaryWeaponCount++
		}
	}

	for _, record := range playerData.Purchases {
		if !isPrimaryWeapon(record.Item) {
			continue
		}

		switch record.Action {
		case ActionPurchase, ActionPickup:
			primaryWeaponCount++
		case ActionDrop:
			primaryWeaponCount--
		}
	}

	shouldRemoveSecondary := primaryWeaponCount >= 1

	optimized := make([]string, 0, len(playerData.InitialInventory))
	for _, item := range playerData.InitialInventory {
		if shouldRemoveSecondary && secondaryPistols[item] {
			continue
		}
		optimized = append(optimized, item)
	}

	return optimized
}

func savePurchaseData(data AllRoundsPurchaseData) error {
	if err := savePurchaseDataRaw(data); err != nil {
		return err
	}

	if err := savePurchaseDataOptimized(data); err != nil {
		return err
	}

	return nil
}

func savePurchaseDataRaw(data AllRoundsPurchaseData) error {
	outputFile := filepath.Join(outputBaseDir, "purchases_raw.json")
	os.MkdirAll(outputBaseDir, 0755)

	orderedData := orderRoundData(data)

	jsonData, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	if err := os.WriteFile(outputFile, jsonData, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	ilog.InfoLogger.Printf("原始购买数据已保存到: %s", outputFile)
	return nil
}

func savePurchaseDataOptimized(data AllRoundsPurchaseData) error {
	outputFile := filepath.Join(outputBaseDir, "purchases.json")

	orderedData := make(map[string]*RoundPurchaseData)
	roundNums := extractAndSortRounds(data)

	for _, num := range roundNums {
		key := fmt.Sprintf("round%d", num)

		optimizedRound := &RoundPurchaseData{
			T:  make(map[string]*PlayerPurchaseData, len(data[key].T)),
			CT: make(map[string]*PlayerPurchaseData, len(data[key].CT)),
		}

		for teamKey, sourceTeam := range map[string]map[string]*PlayerPurchaseData{
			"T":  data[key].T,
			"CT": data[key].CT,
		} {
			var targetTeam map[string]*PlayerPurchaseData
			if teamKey == "T" {
				targetTeam = optimizedRound.T
			} else {
				targetTeam = optimizedRound.CT
			}

			for playerName, playerData := range sourceTeam {
				targetTeam[playerName] = &PlayerPurchaseData{
					InitialInventory:      optimizeInitialInventory(playerData),
					Purchases:             playerData.Purchases,
					FreezetimeEndGrenades: playerData.FreezetimeEndGrenades,
				}
			}
		}

		orderedData[key] = optimizedRound
	}

	jsonData, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	if err := os.WriteFile(outputFile, jsonData, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

func extractAndSortRounds(data AllRoundsPurchaseData) []int {
	roundNums := make([]int, 0, len(data))
	for key := range data {
		var num int
		fmt.Sscanf(key, "round%d", &num)
		roundNums = append(roundNums, num)
	}
	sort.Ints(roundNums)
	return roundNums
}

func orderRoundData(data AllRoundsPurchaseData) map[string]*RoundPurchaseData {
	roundNums := extractAndSortRounds(data)
	orderedData := make(map[string]*RoundPurchaseData, len(roundNums))

	for _, num := range roundNums {
		key := fmt.Sprintf("round%d", num)
		orderedData[key] = data[key]
	}

	return orderedData
}
