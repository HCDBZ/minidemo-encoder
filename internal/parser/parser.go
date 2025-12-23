package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/hx-w/minidemo-encoder/internal/encoder"
	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
	events "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/events"
)

func getTeamPlayers(gs dem.GameState) (tPlayers, ctPlayers []*common.Player) {
	if tTeam := gs.TeamTerrorists(); tTeam != nil {
		tPlayers = tTeam.Members()
	}
	if ctTeam := gs.TeamCounterTerrorists(); ctTeam != nil {
		ctPlayers = ctTeam.Members()
	}
	return tPlayers, ctPlayers
}

func getAllPlayers(gs dem.GameState) []*common.Player {
	tPlayers, ctPlayers := getTeamPlayers(gs)
	return append(tPlayers, ctPlayers...)
}

type TickPlayer struct {
	tick    int
	steamid uint64
}

type RoundInfo struct {
	roundNum        int
	freezetimeStart int
	freezetimeEnd   int
	roundEnd        int
	inFreezeTime    bool
	isHalftime      bool
	started         bool
	buyTimeEnd      int
	dropAllowedTick int
	recordStartTick int
}

type C4HolderInfo struct {
	RoundNum   int    `json:"round"`
	PlayerName string `json:"player_name"`
}

type ChatMessage struct {
	Round      int     `json:"round"`
	Time       float64 `json:"time"`
	PlayerName string  `json:"player_name"`
	Team       string  `json:"team"`
	Message    string  `json:"message"`
	IsTeamChat bool    `json:"is_team_chat"`
}

type PlayerInfo struct {
	SteamID       uint64 `json:"steamid"`
	CrosshairCode string `json:"crosshair_code"`
}

type SpawnPosition struct {
	PlayerName string `json:"player_name"`
	Team       string `json:"team"`
	Position   string `json:"position"`
}

type RoundSpawnData struct {
	Round  int             `json:"round"`
	Spawns []SpawnPosition `json:"spawns"`
}

var (
	allRoundsFreezeInfo   []string
	allFreezeDurations    []float64
	allPurchaseData       AllRoundsPurchaseData
	weaponTracker         *WeaponTracker
	currentRoundPurchases *RoundPurchaseData
	outputBaseDir         string

	detectedTickRate  float64 = 128.0
	detectedFrameRate float64 = 128.0
	timeScaleFactor   float64 = 1.0
	frameRateDetected bool    = false
	needInterpolation bool    = false
	targetFrameRate   float64 = 128.0

	allC4Holders    []C4HolderInfo
	allChatMessages []ChatMessage
	allRoundsSpawns []RoundSpawnData
	allPlayersInfo  map[string]*PlayerInfo

	buttonTickMap map[TickPlayer]int32

	playerLastScopedState map[uint64]bool

	recentPickups map[TickPlayer]int

	purchasedThisTick map[TickPlayer]int64
	recordMode        string  = "late_start" // late_start full
	lateStartOffset   float64 = 1.0

	allRoundsStartPositions RoundStartPositions
)

// 录制开始位置数据
type PlayerStartPosition struct {
	PlayerName string     `json:"player_name"`
	Team       string     `json:"team"`
	Position   [3]float32 `json:"position"`
	AimTarget  [3]float32 `json:"aim_target"`
}

type RoundStartPositions map[string][]PlayerStartPosition

func initializePlayerInRound(player *common.Player, roundPurchases *RoundPurchaseData) {
	if player == nil || roundPurchases == nil {
		return
	}

	playerName := player.Name
	delete(roundPurchases.T, playerName)
	delete(roundPurchases.CT, playerName)

	var teamMap map[string]*PlayerPurchaseData
	if player.Team == common.TeamTerrorists {
		teamMap = roundPurchases.T
	} else if player.Team == common.TeamCounterTerrorists {
		teamMap = roundPurchases.CT
	}

	if teamMap != nil {
		teamMap[playerName] = &PlayerPurchaseData{
			InitialInventory:      getFinalInventory(player),
			Purchases:             []PurchaseRecord{},
			FreezetimeEndGrenades: []string{},
		}
	}
}

// 插帧
func interpolateFrames(playerName string, sourceRate, targetRate, tickrate float64) {
	frames := encoder.PlayerFramesMap[playerName]
	if len(frames) < 2 {
		return
	}

	ratio := targetRate / sourceRate
	if ratio <= 1.0 {
		return
	}

	lastFrame := frames[len(frames)-1]
	prevFrame := frames[len(frames)-2]
	numInterp := int(ratio) - 1

	for i := 1; i <= numInterp; i++ {
		t := float32(i) / float32(numInterp+1)

		interpFrame := encoder.FrameInfo{
			ActualVelocity: [3]float32{
				lerp(prevFrame.ActualVelocity[0], lastFrame.ActualVelocity[0], t),
				lerp(prevFrame.ActualVelocity[1], lastFrame.ActualVelocity[1], t),
				lerp(prevFrame.ActualVelocity[2], lastFrame.ActualVelocity[2], t),
			},
			PredictedVelocity: [3]float32{
				lerp(prevFrame.PredictedVelocity[0], lastFrame.PredictedVelocity[0], t),
				lerp(prevFrame.PredictedVelocity[1], lastFrame.PredictedVelocity[1], t),
				lerp(prevFrame.PredictedVelocity[2], lastFrame.PredictedVelocity[2], t),
			},
			PredictedAngles: [2]float32{
				lerpAngle(prevFrame.PredictedAngles[0], lastFrame.PredictedAngles[0], t),
				lerpAngle(prevFrame.PredictedAngles[1], lastFrame.PredictedAngles[1], t),
			},
			Origin: [3]float32{
				lerp(prevFrame.Origin[0], lastFrame.Origin[0], t),
				lerp(prevFrame.Origin[1], lastFrame.Origin[1], t),
				lerp(prevFrame.Origin[2], lastFrame.Origin[2], t),
			},
			PlayerButtons:    prevFrame.PlayerButtons,
			CSWeaponID:       int32(CSWeapon_NONE),
			AdditionalFields: 0,
		}

		frames = append(frames[:len(frames)-1], interpFrame, lastFrame)
	}

	encoder.PlayerFramesMap[playerName] = frames
}

// 帧率检测
func detectActualFrameRate(parser dem.Parser) {
	var (
		frameSamples  []float64
		lastFrameTick int
		sampleCount   int
		maxSamples    = 500
	)

	parser.RegisterEventHandler(func(e events.FrameDone) {
		gs := parser.GameState()

		if gs.IsWarmupPeriod() || frameRateDetected {
			return
		}

		currentTick := gs.IngameTick()

		if lastFrameTick > 0 {
			tickDiff := currentTick - lastFrameTick
			if tickDiff > 0 && tickDiff < 10 {
				frameSamples = append(frameSamples, float64(tickDiff))
				sampleCount++
			}
		}

		lastFrameTick = currentTick

		if sampleCount >= maxSamples {
			frameRateDetected = true

			var sum float64
			for _, sample := range frameSamples {
				sum += sample
			}
			avgTicksPerFrame := sum / float64(len(frameSamples))

			detectedTickRate = parser.TickRate()
			detectedFrameRate = detectedTickRate / avgTicksPerFrame
			timeScaleFactor = detectedFrameRate / detectedTickRate

			needInterpolation = detectedFrameRate < targetFrameRate
			if needInterpolation {
				ilog.InfoLogger.Printf("将从 %.2f fps 插帧到 %.2f fps", detectedFrameRate, targetFrameRate)
			}

			ilog.InfoLogger.Printf("========== 帧率检测结果 ==========")
			ilog.InfoLogger.Printf("Demo Tick Rate: %.2f", detectedTickRate)
			ilog.InfoLogger.Printf("实际帧率: %.2f fps", detectedFrameRate)
			ilog.InfoLogger.Printf("平均每帧间隔: %.2f ticks", avgTicksPerFrame)
			ilog.InfoLogger.Printf("==================================")

			saveDemoInfo()
		}
	})
}

func getAdjustedTime(tickDiff int, tickRate float64) float64 {
	return float64(tickDiff) / tickRate
}

func saveDemoInfo() {
	infoFile := filepath.Join(outputBaseDir, "demo_info.json")

	info := map[string]interface{}{
		"tick_rate":  detectedTickRate,
		"frame_rate": detectedFrameRate,
		"time_scale": timeScaleFactor,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		ilog.ErrorLogger.Printf("保存demo信息失败: %s\n", err.Error())
		return
	}

	os.WriteFile(infoFile, data, 0644)
	ilog.InfoLogger.Printf("Demo信息已保存到: %s", infoFile)
}

// 判断是否为狙击步枪
func isSniperRifle(weapon *common.Equipment) bool {
	if weapon == nil {
		return false
	}
	switch weapon.Type {
	case common.EqAWP, common.EqScout, common.EqG3SG1, common.EqScar20, common.EqSG556, common.EqAUG:
		return true
	}
	return false
}

// 主解析函数
func Start(filePath string) {
	defer func() {
		if r := recover(); r != nil {
			ilog.ErrorLogger.Printf("解析过程发生严重错误: %v", r)
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			ilog.ErrorLogger.Printf("堆栈信息:\n%s", buf[:n])
		}
	}()

	iFile, err := os.Open(filePath)
	checkError(err)
	defer iFile.Close()

	iParser := dem.NewParser(iFile)
	defer iParser.Close()

	demoFileName := filepath.Base(filePath)
	demoName := strings.TrimSuffix(demoFileName, filepath.Ext(demoFileName))

	outputBaseDir = filepath.Join("output", demoName)
	err = os.MkdirAll(outputBaseDir, os.ModePerm)
	if err != nil {
		ilog.ErrorLogger.Printf("创建输出目录失败: %s\n", err.Error())
		return
	}

	encoder.SetSaveDir(outputBaseDir)
	ilog.InfoLogger.Printf("输出目录: %s", outputBaseDir)

	// 初始化全局变量
	allRoundsFreezeInfo = make([]string, 0)
	allFreezeDurations = make([]float64, 0)
	allPurchaseData = make(AllRoundsPurchaseData)
	weaponTracker = NewWeaponTracker()
	allC4Holders = make([]C4HolderInfo, 0)
	allPlayersInfo = make(map[string]*PlayerInfo)
	allChatMessages = make([]ChatMessage, 0)
	buttonTickMap = make(map[TickPlayer]int32, 1000)
	playerLastScopedState = make(map[uint64]bool)
	recentPickups = make(map[TickPlayer]int)
	purchasedThisTick = make(map[TickPlayer]int64)
	allRoundsStartPositions = make(RoundStartPositions)

	var (
		roundNum     = 0
		currentRound *RoundInfo
	)

	// 帧率检测
	detectActualFrameRate(iParser)

	// FrameDone 每帧处理
	iParser.RegisterEventHandler(func(e events.FrameDone) {
		defer func() {
			if r := recover(); r != nil {
				return
			}
		}()

		gs := iParser.GameState()
		currentTick := gs.IngameTick()

		if gs.IsWarmupPeriod() || currentRound == nil {
			return
		}

		if recordMode == "late_start" {
			if currentTick < currentRound.freezetimeStart {
				return
			}
		}

		Players := getAllPlayers(gs)
		if len(Players) == 0 {
			return
		}

		for _, player := range Players {
			if player == nil || !player.IsAlive() {
				continue
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						return
					}
				}()

				steamID := player.SteamID64
				var addonButton int32 = 0

				if val, ok := buttonTickMap[TickPlayer{currentTick, steamID}]; ok {
					addonButton = val
					delete(buttonTickMap, TickPlayer{currentTick, steamID})
				}

				activeWeapon := player.ActiveWeapon()
				if isSniperRifle(activeWeapon) {
					currentScoped := player.IsScoped()
					if lastScoped, exists := playerLastScopedState[steamID]; exists {
						if currentScoped != lastScoped {
							addonButton |= IN_ATTACK2
						}
					}
					playerLastScopedState[steamID] = currentScoped
				}

				if player.IsDefusing {
					addonButton |= IN_USE
				}
				if player.IsPlanting {
					addonButton |= IN_USE
				}

				parsePlayerFrame(player, addonButton, iParser.TickRate(), currentRound.inFreezeTime)

				if needInterpolation && len(encoder.PlayerFramesMap[player.Name]) >= 2 {
					interpolateFrames(player.Name, detectedFrameRate, targetFrameRate, iParser.TickRate())
				}
			}()
		}
	})

	// 武器物品事件

	// ItemPickup
	iParser.RegisterEventHandler(func(e events.ItemPickup) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() || e.Player == nil || e.Weapon == nil {
			return
		}

		currentTick := gs.IngameTick()
		steamID := e.Player.SteamID64
		weaponName := getEquipmentName(e.Weapon)

		// 记录拾取的槽位
		key := TickPlayer{currentTick, steamID}
		recentPickups[key] = getWeaponSlot(e.Weapon.Type)

		// 判断是购买还是拾起
		isPurchase := false
		isPickup := false

		if weaponTracker.IsNewWeapon(e.Weapon) {
			isPurchase = true
		} else if weaponTracker.IsPickupFromGround(e.Weapon, steamID) {
			isPickup = true
		} else {
			if isGrenadeType(e.Weapon.Type) {
				if currentRound != nil && e.Player.IsInBuyZone() {
					if currentRound.inFreezeTime || currentTick <= currentRound.buyTimeEnd {
						isPurchase = true
					}
				}
			}
		}

		// 购买系统记录
		if currentRound != nil && currentRound.inFreezeTime && !shouldFilterWeapon(weaponName) {
			if isPurchase || isPickup {
				dropTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

				var teamMap map[string]*PlayerPurchaseData
				if e.Player.Team == common.TeamTerrorists {
					teamMap = currentRoundPurchases.T
				} else if e.Player.Team == common.TeamCounterTerrorists {
					teamMap = currentRoundPurchases.CT
				}

				if teamMap != nil {
					playerName := e.Player.Name
					if _, exists := teamMap[playerName]; !exists {
						initializePlayerInRound(e.Player, currentRoundPurchases)
						if e.Player.Team == common.TeamTerrorists {
							teamMap = currentRoundPurchases.T
						} else {
							teamMap = currentRoundPurchases.CT
						}
					}

					action := ActionPickup
					if isPurchase {
						action = ActionPurchase
						key := TickPlayer{currentTick, steamID}
						purchasedThisTick[key] = e.Weapon.UniqueID()
					}

					record := PurchaseRecord{
						Time:   dropTime,
						Item:   weaponName,
						Slot:   getEquipmentSlot(e.Weapon.Type),
						Action: action,
					}
					teamMap[playerName].Purchases = append(teamMap[playerName].Purchases, record)
				}
			}
		}

		// 拾起USE键
		if isPickup && e.Player.IsAlive() {
			pickupKey := TickPlayer{currentTick, steamID}
			if existingButtons, ok := buttonTickMap[pickupKey]; ok {
				buttonTickMap[pickupKey] = existingButtons | IN_USE
			} else {
				buttonTickMap[pickupKey] = IN_USE
			}
		}
	})

	// ItemDrop
	iParser.RegisterEventHandler(func(e events.ItemDrop) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() || e.Player == nil || e.Weapon == nil {
			return
		}

		currentTick := gs.IngameTick()

		// 回合开始1秒内不记录丢弃
		if currentRound != nil && currentTick < currentRound.dropAllowedTick {
			return
		}
		weaponType := e.Weapon.Type
		steamID := e.Player.SteamID64
		weaponName := getEquipmentName(e.Weapon)

		if isGrenadeType(weaponType) {
			return
		}

		// 仅冻结时间购买追踪
		if currentRound != nil && currentRound.inFreezeTime && !shouldFilterWeapon(weaponName) {
			// 检查是否为新武器（RegisterDrop之前）
			isNewWeapon := weaponTracker.IsNewWeapon(e.Weapon)

			weaponTracker.RegisterDrop(e.Weapon, steamID)

			dropTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

			var teamMap map[string]*PlayerPurchaseData
			if e.Player.Team == common.TeamTerrorists {
				teamMap = currentRoundPurchases.T
			} else if e.Player.Team == common.TeamCounterTerrorists {
				teamMap = currentRoundPurchases.CT
			}

			if teamMap != nil {
				playerName := e.Player.Name
				if _, exists := teamMap[playerName]; !exists {
					initializePlayerInRound(e.Player, currentRoundPurchases)
					if e.Player.Team == common.TeamTerrorists {
						teamMap = currentRoundPurchases.T
					} else {
						teamMap = currentRoundPurchases.CT
					}
				}

				// 判断是否为购买丢弃
				action := ActionDrop
				if isNewWeapon && e.Player.IsInBuyZone() {
					action = ActionBuyDrop
				}

				dropRecord := PurchaseRecord{
					Time:   dropTime,
					Item:   weaponName,
					Slot:   getEquipmentSlot(e.Weapon.Type),
					Action: action,
				}
				teamMap[playerName].Purchases = append(teamMap[playerName].Purchases, dropRecord)
			}
		}

		// 丢弃类型的判断

		// 检查是否生成按键
		if e.Player.IsAlive() {
			key := TickPlayer{currentTick, steamID}
			droppedWeaponSlot := getWeaponSlot(weaponType)

			// 检查同一tick是否拾取了同槽位武器（自动替换）
			isAutoReplace := false
			if pickedSlot, exists := recentPickups[key]; exists {
				if pickedSlot == droppedWeaponSlot {
					isAutoReplace = true
				}
			}

			// 检查是否为购买丢弃（Ctrl+购买）
			isBuyDrop := false
			if !isAutoReplace && weaponTracker.IsNewWeapon(e.Weapon) {
				if e.Player.IsInBuyZone() && currentRound != nil {
					if currentRound.inFreezeTime || currentTick <= currentRound.buyTimeEnd {
						isBuyDrop = true
					}
				}
			}

			// 只有手动丢弃才生成按键
			if !isAutoReplace && !isBuyDrop {
				dropButton := encodeDropButton(droppedWeaponSlot)

				if existingButtons, ok := buttonTickMap[key]; ok {
					buttonTickMap[key] = existingButtons | dropButton
				} else {
					buttonTickMap[key] = dropButton
				}
			}
		}
	})

	// WeaponFire
	iParser.RegisterEventHandler(func(e events.WeaponFire) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() || e.Shooter == nil {
			return
		}

		currentTick := gs.IngameTick()
		steamID := e.Shooter.SteamID64
		key := TickPlayer{currentTick, steamID}

		if existingButtons, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] = existingButtons | IN_ATTACK
		} else {
			buttonTickMap[key] = IN_ATTACK
		}
	})

	// PlayerJump
	iParser.RegisterEventHandler(func(e events.PlayerJump) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() || e.Player == nil {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if existingButtons, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] = existingButtons | IN_JUMP
		} else {
			buttonTickMap[key] = IN_JUMP
		}
	})

	// USE事件

	// 拆弹开始
	iParser.RegisterEventHandler(func(e events.BombDefuseStart) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if existingButtons, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] = existingButtons | IN_USE
		} else {
			buttonTickMap[key] = IN_USE
		}
	})

	// 安装炸弹
	iParser.RegisterEventHandler(func(e events.BombPlantBegin) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if existingButtons, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] = existingButtons | IN_USE
		} else {
			buttonTickMap[key] = IN_USE
		}
	})

	// 救援人质
	iParser.RegisterEventHandler(func(e events.HostageRescued) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if existingButtons, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] = existingButtons | IN_USE
		} else {
			buttonTickMap[key] = IN_USE
		}
	})

	// 回合事件

	// RoundStart
	iParser.RegisterEventHandler(func(e events.RoundStart) {
		gs := iParser.GameState()
		currentTick := gs.IngameTick()

		if gs.IsWarmupPeriod() {
			return
		}

		roundNum = gs.TotalRoundsPlayed() + 1

		ilog.InfoLogger.Printf("====================================")
		ilog.InfoLogger.Printf("回合 %d 开始", roundNum)

		tickRate := iParser.TickRate()
		dropDelayTicks := int(tickRate * 1.0)

		var recordStartTick int
		if recordMode == "late_start" {
			recordStartTick = currentTick
			ilog.InfoLogger.Printf("  录制模式: late_start, 将在冻结结束时精确裁剪到前 %.1f 秒", lateStartOffset)
		} else {
			recordStartTick = currentTick
		}

		currentRound = &RoundInfo{
			roundNum:        roundNum,
			freezetimeStart: currentTick,
			freezetimeEnd:   currentTick,
			inFreezeTime:    true,
			isHalftime:      false,
			started:         true,
			dropAllowedTick: currentTick + dropDelayTicks,
			recordStartTick: recordStartTick,
		}

		currentRoundPurchases = &RoundPurchaseData{
			T:  make(map[string]*PlayerPurchaseData),
			CT: make(map[string]*PlayerPurchaseData),
		}
		allPurchaseData[fmt.Sprintf("round%d", roundNum)] = currentRoundPurchases

		weaponTracker = NewWeaponTracker()
		playerLastScopedState = make(map[uint64]bool)

		Players := getAllPlayers(gs)
		for _, player := range Players {
			if player != nil {
				parsePlayerInitFrame(player)
				recordPlayerStartMoney(player, roundNum)
				recordPlayerInfo(player)
				initializePlayerInRound(player, currentRoundPurchases)
			}
		}

		ilog.InfoLogger.Printf("  已初始化 %d 名玩家", len(Players))
	})

	// RoundFreezetimeEnd
	iParser.RegisterEventHandler(func(e events.RoundFreezetimeEnd) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() {
			return
		}

		if currentRound != nil {
			currentTick := gs.IngameTick()

			currentRound.freezetimeEnd = currentTick
			currentRound.inFreezeTime = false

			tickRate := iParser.TickRate()

			if recordMode == "late_start" {
				offsetTicks := int(tickRate * lateStartOffset)
				actualStartTick := currentTick - offsetTicks

				Players := getAllPlayers(gs)
				trimmedCount := 0
				for _, player := range Players {
					if player == nil {
						continue
					}

					frames := encoder.PlayerFramesMap[player.Name]
					if len(frames) == 0 {
						continue
					}

					if len(frames) > offsetTicks {
						encoder.PlayerFramesMap[player.Name] = frames[len(frames)-offsetTicks:]
						trimmedCount++
					}
				}

				currentRound.recordStartTick = actualStartTick
				ilog.InfoLogger.Printf("  精确裁剪完成: 保留冻结结束前 %.1f 秒的数据 (%d ticks, %d 名玩家)",
					lateStartOffset, offsetTicks, trimmedCount)
			}

			extendTicks := int(tickRate * 20)
			currentRound.buyTimeEnd = currentTick + extendTicks

			ilog.InfoLogger.Printf("回合 %d 冻结时间结束", currentRound.roundNum)

			if recordMode == "late_start" {
				recordRoundStartPositions(&gs, currentRound.roundNum)
			}

			recordPlayersGrenades(&gs, currentRoundPurchases)
			recordPlayerSpawns(&gs, currentRound.roundNum)
			detectC4Holder(&gs, currentRound.roundNum)
		}
	})

	// RoundEnd
	iParser.RegisterEventHandler(func(e events.RoundEnd) {
		gs := iParser.GameState()
		currentTick := gs.IngameTick()

		if gs.IsWarmupPeriod() {
			currentRound = nil
			return
		}

		if currentRound == nil {
			ilog.InfoLogger.Printf("收到 RoundEnd 但 currentRound 为 nil")
			return
		}

		currentRound.roundEnd = currentTick
		freezeDuration := getAdjustedTime(currentRound.freezetimeEnd-currentRound.freezetimeStart, iParser.TickRate())

		ilog.InfoLogger.Printf("回合 %d 结束", currentRound.roundNum)

		if currentRound.isHalftime {
			freezeInfo := fmt.Sprintf("HALFTIME:%d", currentRound.roundNum)
			allRoundsFreezeInfo = append(allRoundsFreezeInfo, freezeInfo)
		} else {
			freezeInfo := fmt.Sprintf("round%d: %.2f秒", currentRound.roundNum, freezeDuration)
			allRoundsFreezeInfo = append(allRoundsFreezeInfo, freezeInfo)
			allFreezeDurations = append(allFreezeDurations, freezeDuration)
		}

		adjustMoneyForInitialInventory(currentRoundPurchases, currentRound.roundNum)

		Players := getAllPlayers(gs)
		savedCount := 0
		for _, player := range Players {
			if player != nil {
				frameCount := len(encoder.PlayerFramesMap[player.Name])
				if frameCount == 0 {
					continue
				}
				saveToRecFile(player, int32(currentRound.roundNum))
				savedCount++
			}
		}

		ilog.InfoLogger.Printf("  已保存 %d 个玩家录像到: %s/round%d/", savedCount, outputBaseDir, currentRound.roundNum)

		buttonTickMap = make(map[TickPlayer]int32, 1000)
		recentPickups = make(map[TickPlayer]int)
		purchasedThisTick = make(map[TickPlayer]int64)

		ilog.InfoLogger.Printf("  已清理按键映射和临时数据")

		currentRound = nil
	})

	// GameHalfEnded 半场结束
	iParser.RegisterEventHandler(func(e events.GameHalfEnded) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() {
			return
		}

		if currentRound != nil {
			currentRound.isHalftime = true
		}
	})

	// ChatMessage 聊天消息
	iParser.RegisterEventHandler(func(e events.ChatMessage) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() {
			return
		}

		if recordMode == "late_start" {
			return
		}

		if currentRound == nil || !currentRound.started {
			return
		}

		currentTick := gs.IngameTick()
		chatTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

		teamName := "Unknown"
		var sender *common.Player

		allPlayers := getAllPlayers(gs)
		for _, player := range allPlayers {
			if player != nil && player.Name == e.Sender.Name {
				sender = player
				break
			}
		}

		if sender != nil {
			switch sender.Team {
			case common.TeamTerrorists:
				teamName = "T"
			case common.TeamCounterTerrorists:
				teamName = "CT"
			case common.TeamSpectators:
				teamName = "Spectator"
			default:
				teamName = "Unknown"
			}
		}

		chatMsg := ChatMessage{
			Round:      currentRound.roundNum,
			Time:       chatTime,
			PlayerName: e.Sender.Name,
			Team:       teamName,
			Message:    e.Text,
			IsTeamChat: !e.IsChatAll,
		}

		allChatMessages = append(allChatMessages, chatMsg)
	})

	err = iParser.ParseToEnd()
	checkError(err)

	saveFreezeTimeInfo()

	ilog.InfoLogger.Println("\n开始保存购买数据...")
	for roundKey, roundData := range allPurchaseData {
		tTotal := 0
		ctTotal := 0
		for _, pdata := range roundData.T {
			tTotal += len(pdata.Purchases)
		}
		for _, pdata := range roundData.CT {
			ctTotal += len(pdata.Purchases)
		}
		ilog.InfoLogger.Printf("  %s: T方 %d 条记录, CT方 %d 条记录", roundKey, tTotal, ctTotal)
	}

	err = savePurchaseData(allPurchaseData)
	if err != nil {
		ilog.ErrorLogger.Printf("保存购买数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("购买数据已保存到: %s/purchases.json (优化版)", outputBaseDir)
		ilog.InfoLogger.Printf("原始购买数据已保存到: %s/purchases_raw.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存金钱数据...")
	err = saveMoneyData()
	if err != nil {
		ilog.ErrorLogger.Printf("保存金钱数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("金钱数据已保存到: %s/money.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存C4持有者数据...")
	err = saveC4HolderData()
	if err != nil {
		ilog.ErrorLogger.Printf("保存C4数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("C4数据已保存到: %s/c4_holders.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存玩家信息数据...")
	err = savePlayersInfo()
	if err != nil {
		ilog.ErrorLogger.Printf("保存玩家信息失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("玩家信息已保存到: %s/players_info.json", outputBaseDir)
	}

	if recordMode != "late_start" {
		ilog.InfoLogger.Println("\n开始保存聊天数据...")
		err = saveChatData()
		if err != nil {
			ilog.ErrorLogger.Printf("保存聊天数据失败: %s\n", err.Error())
		} else {
			ilog.InfoLogger.Printf("聊天数据已保存到: %s/chat.json", outputBaseDir)
			ilog.InfoLogger.Printf("共记录 %d 条聊天消息", len(allChatMessages))
		}
	}

	if recordMode != "late_start" {
		ilog.InfoLogger.Println("\n开始保存出生点数据...")
		err = saveSpawnData()
		if err != nil {
			ilog.ErrorLogger.Printf("保存出生点数据失败: %s\n", err.Error())
		} else {
			ilog.InfoLogger.Printf("出生点数据已保存到: %s/spawns.json", outputBaseDir)
			ilog.InfoLogger.Printf("共记录 %d 个回合的出生点", len(allRoundsSpawns))
		}
	}

	ilog.InfoLogger.Println("\n开始保存录制开始位置数据...")
	err = saveRecordStartPositions()
	if err != nil {
		ilog.ErrorLogger.Printf("保存录制开始位置失败: %s\n", err.Error())
	} else if recordMode == "late_start" {
		ilog.InfoLogger.Printf("录制开始位置已保存到: %s/record_start_positions.json", outputBaseDir)
		ilog.InfoLogger.Printf("共记录 %d 个回合的开始位置", len(allRoundsStartPositions))
	}
}

func saveFreezeTimeInfo() {
	if recordMode == "late_start" {
		ilog.InfoLogger.Println("\nlate_start 模式: 跳过冻结时间信息保存")
		return
	}

	mostCommonFreeze := getMostCommonFreezeDuration()

	for i, info := range allRoundsFreezeInfo {
		if len(info) >= 9 && info[:9] == "HALFTIME:" {
			var roundNum int
			fmt.Sscanf(info, "HALFTIME:%d", &roundNum)
			allRoundsFreezeInfo[i] = fmt.Sprintf("round%d: %.2f秒", roundNum, mostCommonFreeze)
		}
	}

	freezeFile := filepath.Join(outputBaseDir, "freeze.txt")
	file, err := os.Create(freezeFile)
	if err != nil {
		ilog.ErrorLogger.Printf("创建 freeze.txt 失败: %s\n", err.Error())
		return
	}
	defer file.Close()

	for _, info := range allRoundsFreezeInfo {
		var roundNum int
		var duration float64
		if _, err := fmt.Sscanf(info, "回合 %d: %f秒", &roundNum, &duration); err == nil {
			file.WriteString(fmt.Sprintf("round%d: %.2f秒\n", roundNum, duration))
		} else {
			file.WriteString(info + "\n")
		}
	}

	file.WriteString(fmt.Sprintf("冻结时间:%.2f秒", mostCommonFreeze))

	ilog.InfoLogger.Printf("\n冻结时间信息已保存到: %s", freezeFile)
	ilog.InfoLogger.Printf("(半场回合使用最常见冻结时间: %.2f秒)\n", mostCommonFreeze)
}

func getMostCommonFreezeDuration() float64 {
	if len(allFreezeDurations) == 0 {
		return 15.0
	}

	countMap := make(map[int]int)
	for _, duration := range allFreezeDurations {
		rounded := int(duration*10 + 0.5)
		countMap[rounded]++
	}

	maxCount := 0
	mostCommon := 150
	for duration, count := range countMap {
		if count > maxCount {
			maxCount = count
			mostCommon = duration
		}
	}

	return float64(mostCommon) / 10.0
}

func detectC4Holder(gs *dem.GameState, roundNum int) {
	tPlayers, _ := getTeamPlayers(*gs)

	for _, player := range tPlayers {
		if player == nil {
			continue
		}

		for _, weapon := range player.Weapons() {
			if weapon != nil && weapon.Type == common.EqBomb {
				c4Info := C4HolderInfo{
					RoundNum:   roundNum,
					PlayerName: player.Name,
				}
				allC4Holders = append(allC4Holders, c4Info)
				return
			}
		}
	}
}

func recordPlayerInfo(player *common.Player) {
	if player == nil {
		return
	}

	playerName := player.Name

	if _, exists := allPlayersInfo[playerName]; exists {
		return
	}

	crosshairCode := player.CrosshairCode()
	if crosshairCode == "" {
		crosshairCode = "N/A"
	}

	allPlayersInfo[playerName] = &PlayerInfo{
		SteamID:       player.SteamID64,
		CrosshairCode: crosshairCode,
	}

	ilog.InfoLogger.Printf("  [玩家信息] %s - SteamID: %d, 准星: %s",
		playerName, player.SteamID64, crosshairCode)
}

func saveC4HolderData() error {
	c4File := filepath.Join(outputBaseDir, "c4_holders.json")

	data, err := json.MarshalIndent(allC4Holders, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(c4File, data, 0644)
}

func savePlayersInfo() error {
	playersFile := filepath.Join(outputBaseDir, "players_info.json")

	data, err := json.MarshalIndent(allPlayersInfo, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(playersFile, data, 0644)
}

func saveChatData() error {
	chatFile := filepath.Join(outputBaseDir, "chat.json")

	data, err := json.MarshalIndent(allChatMessages, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(chatFile, data, 0644)
}

func saveSpawnData() error {
	spawnFile := filepath.Join(outputBaseDir, "spawns.json")

	tSpawns := make(map[string]bool)
	ctSpawns := make(map[string]bool)

	for _, roundData := range allRoundsSpawns {
		for _, spawn := range roundData.Spawns {
			if spawn.Team == "T" {
				tSpawns[spawn.Position] = true
			} else if spawn.Team == "CT" {
				ctSpawns[spawn.Position] = true
			}
		}
	}

	tList := make([]string, 0, len(tSpawns))
	for pos := range tSpawns {
		tList = append(tList, pos)
	}
	sort.Strings(tList)

	ctList := make([]string, 0, len(ctSpawns))
	for pos := range ctSpawns {
		ctList = append(ctList, pos)
	}
	sort.Strings(ctList)

	output := map[string]interface{}{
		"rounds": allRoundsSpawns,
		"summary": map[string]interface{}{
			"T": map[string]interface{}{
				"count":     len(tList),
				"positions": tList,
			},
			"CT": map[string]interface{}{
				"count":     len(ctList),
				"positions": ctList,
			},
		},
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	return os.WriteFile(spawnFile, data, 0644)
}

func recordPlayersGrenades(gs *dem.GameState, roundPurchases *RoundPurchaseData) {
	allPlayers := getAllPlayers(*gs)

	for _, player := range allPlayers {
		if player == nil {
			continue
		}

		var teamMap map[string]*PlayerPurchaseData
		if player.Team == common.TeamTerrorists {
			teamMap = roundPurchases.T
		} else if player.Team == common.TeamCounterTerrorists {
			teamMap = roundPurchases.CT
		} else {
			continue
		}

		playerName := player.Name

		if _, exists := teamMap[playerName]; !exists {
			teamMap[playerName] = &PlayerPurchaseData{
				InitialInventory:      []string{},
				Purchases:             []PurchaseRecord{},
				FreezetimeEndGrenades: []string{},
			}
		}

		grenades := []string{}
		for _, weapon := range player.Weapons() {
			if weapon != nil {
				weaponName := getEquipmentName(weapon)
				if isGrenadeType(weapon.Type) {
					grenades = append(grenades, weaponName)
				}
			}
		}

		teamMap[playerName].FreezetimeEndGrenades = grenades
	}
}

func recordPlayerSpawns(gs *dem.GameState, roundNum int) {
	if recordMode == "late_start" {
		return
	}

	allPlayers := getAllPlayers(*gs)

	spawns := make([]SpawnPosition, 0, len(allPlayers))

	for _, player := range allPlayers {
		if player == nil || !player.IsAlive() {
			continue
		}

		pos := player.Position()
		teamName := ""
		switch player.Team {
		case common.TeamTerrorists:
			teamName = "T"
		case common.TeamCounterTerrorists:
			teamName = "CT"
		default:
			continue
		}

		spawn := SpawnPosition{
			PlayerName: player.Name,
			Team:       teamName,
			Position:   fmt.Sprintf("%.1f %.1f %.1f", pos.X, pos.Y, pos.Z),
		}

		spawns = append(spawns, spawn)
	}

	roundSpawn := RoundSpawnData{
		Round:  roundNum,
		Spawns: spawns,
	}

	allRoundsSpawns = append(allRoundsSpawns, roundSpawn)

	ilog.InfoLogger.Printf("  记录了 %d 个玩家的出生位置", len(spawns))
}

func getWeaponSlot(eqType common.EquipmentType) int {
	switch eqType {
	case common.EqMP7, common.EqMP9, common.EqMP5, common.EqUMP,
		common.EqP90, common.EqBizon, common.EqMac10,
		common.EqAK47, common.EqM4A4, common.EqM4A1, common.EqGalil,
		common.EqFamas, common.EqAUG, common.EqSG556,
		common.EqAWP, common.EqScout, common.EqScar20, common.EqG3SG1,
		common.EqXM1014, common.EqMag7, common.EqSawedOff, common.EqNova,
		common.EqM249, common.EqNegev:
		return 0

	case common.EqP2000, common.EqGlock, common.EqP250, common.EqDeagle,
		common.EqFiveSeven, common.EqTec9, common.EqCZ, common.EqUSP,
		common.EqRevolver, common.EqDualBerettas:
		return 1

	case common.EqKnife:
		return 2

	case common.EqFlash, common.EqSmoke, common.EqHE,
		common.EqMolotov, common.EqIncendiary, common.EqDecoy:
		return 3

	case common.EqBomb:
		return 4

	case common.EqZeus, common.EqDefuseKit:
		return 5

	default:
		return -1
	}
}

func encodeDropButton(slot int) int32 {
	var slotEncoded int32
	if slot >= 0 {
		slotEncoded = int32(slot + 1)
	} else {
		slotEncoded = 0
	}

	return int32(uint32(IN_DROP) | (uint32(slotEncoded) << 27))
}

// 记录回合开始录制时的位置
func recordRoundStartPositions(gs *dem.GameState, roundNum int) {
	allPlayers := getAllPlayers(*gs)
	positions := make([]PlayerStartPosition, 0, len(allPlayers))

	for _, player := range allPlayers {
		if player == nil || !player.IsAlive() {
			continue
		}

		pos := player.Position()
		teamName := ""
		switch player.Team {
		case common.TeamTerrorists:
			teamName = "T"
		case common.TeamCounterTerrorists:
			teamName = "CT"
		default:
			continue
		}

		// 获取准星瞄准点
		aimTarget := getPlayerAimTarget(player)

		startPos := PlayerStartPosition{
			PlayerName: player.Name,
			Team:       teamName,
			Position:   [3]float32{float32(pos.X), float32(pos.Y), float32(pos.Z)},
			AimTarget:  aimTarget,
		}

		positions = append(positions, startPos)
	}

	roundKey := fmt.Sprintf("round%d", roundNum)
	allRoundsStartPositions[roundKey] = positions

	ilog.InfoLogger.Printf("  记录了 %d 个玩家的录制开始位置和准星坐标", len(positions))
}

// 保存录制开始位置数据
func saveRecordStartPositions() error {
	if recordMode != "late_start" {
		return nil
	}

	startPosFile := filepath.Join(outputBaseDir, "record_start_positions.json")

	roundKeys := make([]string, 0, len(allRoundsStartPositions))
	for key := range allRoundsStartPositions {
		roundKeys = append(roundKeys, key)
	}
	sort.Strings(roundKeys)

	orderedData := make(map[string][]PlayerStartPosition)
	for _, key := range roundKeys {
		orderedData[key] = allRoundsStartPositions[key]
	}

	data, err := json.MarshalIndent(orderedData, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	return os.WriteFile(startPosFile, data, 0644)
}
