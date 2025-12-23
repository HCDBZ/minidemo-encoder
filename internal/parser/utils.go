package parser

import (
	"math"

	encoder "github.com/hx-w/minidemo-encoder/internal/encoder"
	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
)

const Pi = 3.14159265358979323846
const MaxMoveSpeed = 450.0

var bufWeaponMap map[string]int32 = make(map[string]int32)
var playerLastZ map[string]float32 = make(map[string]float32)

type PlayerLastFrame struct {
	Position  [3]float32
	Velocity  [3]float32
	ViewAngle float64
}

var playerLastFrame map[string]*PlayerLastFrame = make(map[string]*PlayerLastFrame)

func checkError(err error) {
	if err != nil {
		ilog.ErrorLogger.Println(err.Error())
		panic(err)
	}
}

func parsePlayerInitFrame(player *common.Player) {
	if player == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			ilog.WarningLogger.Printf("解析玩家初始帧失败 (%s): %v", player.Name, r)
		}
	}()

	iFrameInit := encoder.FrameInitInfo{
		PlayerName: player.Name,
	}

	pos := player.Position()
	iFrameInit.Position[0] = float32(pos.X)
	iFrameInit.Position[1] = float32(pos.Y)
	iFrameInit.Position[2] = float32(pos.Z)

	iFrameInit.Angles[0] = float32(player.ViewDirectionY())
	iFrameInit.Angles[1] = float32(player.ViewDirectionX())

	encoder.InitPlayer(iFrameInit)
	delete(bufWeaponMap, player.Name)
	delete(encoder.PlayerFramesMap, player.Name)

	playerLastZ[player.Name] = float32(pos.Z)

	vel := player.Velocity()
	playerLastFrame[player.Name] = &PlayerLastFrame{
		Position:  [3]float32{float32(pos.X), float32(pos.Y), float32(pos.Z)},
		Velocity:  [3]float32{float32(vel.X), float32(vel.Y), float32(vel.Z)},
		ViewAngle: float64(player.ViewDirectionY()),
	}

	teamName := "T"
	if player.Team == common.TeamCounterTerrorists {
		teamName = "CT"
	}
	ilog.InfoLogger.Printf("  初始化玩家: %s (%s) at (%.1f, %.1f, %.1f)",
		player.Name, teamName, pos.X, pos.Y, pos.Z)
}

func normalizeDegree(degree float64) float64 {
	for degree >= 360.0 {
		degree -= 360.0
	}
	for degree < 0.0 {
		degree += 360.0
	}
	return degree
}

func radian2degree(radian float64) float64 {
	return normalizeDegree(radian * 180 / Pi)
}

func worldVelocityToMoveInput(worldVelX, worldVelY, viewAngleY float64) (moveForward, moveRight float32) {
	velMag := math.Sqrt(worldVelX*worldVelX + worldVelY*worldVelY)

	if velMag < 1.0 {
		return 0, 0
	}

	angleRad := viewAngleY * Pi / 180.0
	sinAngle := math.Sin(angleRad)
	cosAngle := math.Cos(angleRad)

	relForward := worldVelX*cosAngle + worldVelY*sinAngle
	relRight := -worldVelX*sinAngle + worldVelY*cosAngle

	const MAX_GROUND_SPEED = 250.0

	inputMagnitude := math.Min(velMag/MAX_GROUND_SPEED, 1.0) * MaxMoveSpeed

	dirMag := math.Sqrt(relForward*relForward + relRight*relRight)
	if dirMag > 0.01 {
		relForward /= dirMag
		relRight /= dirMag
	}

	moveForward = float32(relForward * inputMagnitude)
	moveRight = float32(-relRight * inputMagnitude)

	return moveForward, moveRight
}

func shouldSetKeyframe(playerName string, currentIdx int, tickrate float64, fullsnap bool,
	currentPos, lastPos [3]float32, currentVel, lastVel [3]float32) bool {

	if fullsnap {
		return true
	}

	if currentIdx > 0 && currentIdx%128 == 0 {
		return true
	}

	if currentIdx > 0 {
		dt := 1.0 / tickrate
		predictX := lastPos[0] + lastVel[0]*float32(dt)
		predictY := lastPos[1] + lastVel[1]*float32(dt)

		errorX := currentPos[0] - predictX
		errorY := currentPos[1] - predictY
		posError := math.Sqrt(float64(errorX*errorX + errorY*errorY))

		if posError > 2.0 {
			return true
		}
	}

	velChangeX := currentVel[0] - lastVel[0]
	velChangeY := currentVel[1] - lastVel[1]
	velChange := math.Sqrt(float64(velChangeX*velChangeX + velChangeY*velChangeY))

	if velChange > 100.0 {
		return true
	}

	return false
}

func parsePlayerFrame(player *common.Player, addonButton int32, tickrate float64, fullsnap bool) {
	if player == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			return
		}
	}()

	if !player.IsAlive() {
		return
	}

	iFrameInfo := new(encoder.FrameInfo)
	playerName := player.Name

	vel := player.Velocity()
	pos := player.Position()
	viewAngleY := player.ViewDirectionY()
	viewAngleX := player.ViewDirectionX()

	// ActualVelocity: 实际速度
	iFrameInfo.ActualVelocity[0] = float32(vel.X)
	iFrameInfo.ActualVelocity[1] = float32(vel.Y)

	lastZ, hasLastZ := playerLastZ[playerName]
	if hasLastZ {
		deltaZ := float32(pos.Z) - lastZ
		iFrameInfo.ActualVelocity[2] = deltaZ * float32(tickrate)
	} else {
		iFrameInfo.ActualVelocity[2] = float32(vel.Z)
	}
	playerLastZ[playerName] = float32(pos.Z)

	// PredictedVelocity: 输入向量
	moveForward, moveRight := worldVelocityToMoveInput(
		float64(vel.X),
		float64(vel.Y),
		float64(viewAngleY),
	)

	iFrameInfo.PredictedVelocity[0] = moveForward
	iFrameInfo.PredictedVelocity[1] = moveRight
	iFrameInfo.PredictedVelocity[2] = iFrameInfo.ActualVelocity[2]

	// 视角
	iFrameInfo.PredictedAngles[0] = viewAngleY
	iFrameInfo.PredictedAngles[1] = viewAngleX

	// 位置
	iFrameInfo.Origin[0] = float32(pos.X)
	iFrameInfo.Origin[1] = float32(pos.Y)
	iFrameInfo.Origin[2] = float32(pos.Z)

	iFrameInfo.PlayerImpulse = 0
	iFrameInfo.PlayerSeed = 0
	iFrameInfo.PlayerSubtype = 0
	iFrameInfo.PlayerButtons = ButtonConvert(player, addonButton)

	// 武器编码
	var currWeaponID int32 = 0
	activeWeapon := player.ActiveWeapon()
	if activeWeapon != nil {
		currWeaponID = int32(WeaponStr2ID(activeWeapon.String()))
	}

	if len(encoder.PlayerFramesMap[playerName]) == 0 {
		iFrameInfo.CSWeaponID = currWeaponID
		bufWeaponMap[playerName] = currWeaponID
	} else if currWeaponID == bufWeaponMap[playerName] {
		iFrameInfo.CSWeaponID = int32(CSWeapon_NONE)
	} else {
		iFrameInfo.CSWeaponID = currWeaponID
		bufWeaponMap[playerName] = currWeaponID
	}

	// 关键帧
	currentIdx := len(encoder.PlayerFramesMap[playerName])

	var lastPos [3]float32
	var lastVel [3]float32

	lastFrame, hasLastFrame := playerLastFrame[playerName]
	if hasLastFrame {
		lastPos = lastFrame.Position
		lastVel = lastFrame.Velocity
	}

	currentPos := [3]float32{float32(pos.X), float32(pos.Y), float32(pos.Z)}
	currentVel := [3]float32{float32(vel.X), float32(vel.Y), float32(vel.Z)}

	if shouldSetKeyframe(playerName, currentIdx, tickrate, fullsnap, currentPos, lastPos, currentVel, lastVel) {
		iFrameInfo.AdditionalFields |= encoder.FIELDS_ORIGIN
		iFrameInfo.AtOrigin[0] = float32(pos.X)
		iFrameInfo.AtOrigin[1] = float32(pos.Y)
		iFrameInfo.AtOrigin[2] = float32(pos.Z)

		iFrameInfo.AdditionalFields |= encoder.FIELDS_VELOCITY
		iFrameInfo.AtVelocity[0] = float32(vel.X)
		iFrameInfo.AtVelocity[1] = float32(vel.Y)
		iFrameInfo.AtVelocity[2] = iFrameInfo.ActualVelocity[2]
	}

	playerLastFrame[playerName] = &PlayerLastFrame{
		Position:  currentPos,
		Velocity:  currentVel,
		ViewAngle: float64(viewAngleY),
	}

	encoder.PlayerFramesMap[playerName] = append(encoder.PlayerFramesMap[playerName], *iFrameInfo)
}

func saveToRecFile(player *common.Player, roundNum int32) {
	if player == nil {
		return
	}

	teamSuffix := "t"
	if player.Team == common.TeamCounterTerrorists {
		teamSuffix = "ct"
	}

	encoder.WriteToRecFile(player.Name, roundNum, teamSuffix)
}

func lerp(a, b, t float32) float32 {
	return a + (b-a)*t
}

func catmullRom(p0, p1, p2, p3, t float32) float32 {
	t2 := t * t
	t3 := t2 * t

	return 0.5 * ((2.0 * p1) +
		(-p0+p2)*t +
		(2.0*p0-5.0*p1+4.0*p2-p3)*t2 +
		(-p0+3.0*p1-3.0*p2+p3)*t3)
}

func lerpAngle(a, b, t float32) float32 {
	diff := b - a

	if diff > 180 {
		diff -= 360
	} else if diff < -180 {
		diff += 360
	}

	result := a + diff*t

	for result > 180 {
		result -= 360
	}
	for result < -180 {
		result += 360
	}

	return result
}

// getPlayerAimTarget 获取玩家准星瞄准点
func getPlayerAimTarget(player *common.Player) [3]float32 {
	if player == nil {
		return [3]float32{0, 0, 0}
	}

	// 获取眼睛位置（SourcePawn的GetClientEyePosition）
	pos := player.Position()
	// 玩家视线高度?
	eyeHeight := float32(64.0)
	if player.IsDucking() {
		eyeHeight = 46.0 // 蹲下时的视线高度
	}

	eyeX := float32(pos.X)
	eyeY := float32(pos.Y)
	eyeZ := float32(pos.Z) + eyeHeight

	// 获取视角（SourcePawn的GetClientEyeAngles）
	pitch := float64(player.ViewDirectionX()) * Pi / 180.0
	yaw := float64(player.ViewDirectionY()) * Pi / 180.0

	// 计算射线方向向量
	cosPitch := math.Cos(pitch)
	dirX := math.Cos(yaw) * cosPitch
	dirY := math.Sin(yaw) * cosPitch
	dirZ := -math.Sin(pitch)

	// 射线长度
	maxDistance := 8192.0

	// 计算终点
	endX := float64(eyeX) + dirX*maxDistance
	endY := float64(eyeY) + dirY*maxDistance
	endZ := float64(eyeZ) + dirZ*maxDistance

	return [3]float32{float32(endX), float32(endY), float32(endZ)}
}
