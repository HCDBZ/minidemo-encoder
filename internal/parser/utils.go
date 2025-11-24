package parser

import (
	"math"

	encoder "github.com/hx-w/minidemo-encoder/internal/encoder"
	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	common "github.com/markus-wa/demoinfocs-golang/v2/pkg/demoinfocs/common"
)

const Pi = 3.14159265358979323846

var bufWeaponMap map[string]int32 = make(map[string]int32)
var playerLastZ map[string]float32 = make(map[string]float32)

// Function to handle errors
func checkError(err error) {
	if err != nil {
		ilog.ErrorLogger.Println(err.Error())
		panic(err)
	}
}

func parsePlayerInitFrame(player *common.Player) {
	iFrameInit := encoder.FrameInitInfo{
		PlayerName: player.Name,
	}
	iFrameInit.Position[0] = float32(player.Position().X)
	iFrameInit.Position[1] = float32(player.Position().Y)
	iFrameInit.Position[2] = float32(player.Position().Z)
	iFrameInit.Angles[0] = float32(player.ViewDirectionY())
	iFrameInit.Angles[1] = float32(player.ViewDirectionX())

	encoder.InitPlayer(iFrameInit)
	delete(bufWeaponMap, player.Name)
	delete(encoder.PlayerFramesMap, player.Name)

	playerLastZ[player.Name] = float32(player.Position().Z)

	// 记录玩家初始化信息
	teamName := "T"
	if player.Team == common.TeamCounterTerrorists {
		teamName = "CT"
	}
	ilog.InfoLogger.Printf("  初始化玩家: %s (%s) at (%.1f, %.1f, %.1f)",
		player.Name, teamName, player.Position().X, player.Position().Y, player.Position().Z)
}

func normalizeDegree(degree float64) float64 {
	if degree < 0.0 {
		degree = degree + 360.0
	}
	return degree
}

// accept radian, return degree in [0, 360)
func radian2degree(radian float64) float64 {
	return normalizeDegree(radian * 180 / Pi)
}

func parsePlayerFrame(player *common.Player, addonButton int32, tickrate float64, fullsnap bool) {
	if !player.IsAlive() {
		return
	}
	iFrameInfo := new(encoder.FrameInfo)
	iFrameInfo.PredictedVelocity[0] = 0.0
	iFrameInfo.PredictedVelocity[1] = 0.0
	iFrameInfo.PredictedVelocity[2] = 0.0
	iFrameInfo.ActualVelocity[0] = float32(player.Velocity().X)
	iFrameInfo.ActualVelocity[1] = float32(player.Velocity().Y)
	iFrameInfo.ActualVelocity[2] = float32(player.Velocity().Z)
	iFrameInfo.PredictedAngles[0] = player.ViewDirectionY()
	iFrameInfo.PredictedAngles[1] = player.ViewDirectionX()

	// 每帧都记录位置
	iFrameInfo.Origin[0] = float32(player.Position().X)
	iFrameInfo.Origin[1] = float32(player.Position().Y)
	iFrameInfo.Origin[2] = float32(player.Position().Z)

	iFrameInfo.PlayerImpulse = 0
	iFrameInfo.PlayerSeed = 0
	iFrameInfo.PlayerSubtype = 0
	// ----- button encode
	iFrameInfo.PlayerButtons = ButtonConvert(player, addonButton)

	// ---- weapon encode
	var currWeaponID int32 = 0
	if player.ActiveWeapon() != nil {
		currWeaponID = int32(WeaponStr2ID(player.ActiveWeapon().String()))
	}
	if len(encoder.PlayerFramesMap[player.Name]) == 0 {
		iFrameInfo.CSWeaponID = currWeaponID
		bufWeaponMap[player.Name] = currWeaponID
	} else if currWeaponID == bufWeaponMap[player.Name] {
		iFrameInfo.CSWeaponID = int32(CSWeapon_NONE)
	} else {
		iFrameInfo.CSWeaponID = currWeaponID
		bufWeaponMap[player.Name] = currWeaponID
	}

	lastIdx := len(encoder.PlayerFramesMap[player.Name]) - 1
	// addons - 在冻结时间或每2秒添加关键帧
	if fullsnap || (lastIdx+1)%int(tickrate*2) == 0 {
		iFrameInfo.AdditionalFields |= encoder.FIELDS_ORIGIN
		iFrameInfo.AtOrigin[0] = float32(player.Position().X)
		iFrameInfo.AtOrigin[1] = float32(player.Position().Y)
		iFrameInfo.AtOrigin[2] = float32(player.Position().Z)

		iFrameInfo.AdditionalFields |= encoder.FIELDS_VELOCITY
		iFrameInfo.AtVelocity[0] = float32(player.Velocity().X)
		iFrameInfo.AtVelocity[1] = float32(player.Velocity().Y)
		iFrameInfo.AtVelocity[2] = float32(player.Velocity().Z)
	}
	// record Z velocity
	deltaZ := float32(player.Position().Z) - playerLastZ[player.Name]
	playerLastZ[player.Name] = float32(player.Position().Z)

	// velocity in Z direction need to be recorded specially
	iFrameInfo.ActualVelocity[2] = deltaZ * float32(tickrate)

	// Since I don't know how to get player's button bits in a tick frame,
	// I have to use *actual vels* and *angles* to generate *predicted vels* approximately
	// This will cause some error, but it's not a big deal
	if lastIdx >= 0 { // not first frame
		// We assume that actual velocity in tick N
		// is influenced by predicted velocity in tick N-1
		_preVel := &encoder.PlayerFramesMap[player.Name][lastIdx].PredictedVelocity

		// PV = 0.0 when AV(tick N-1) = 0.0 and AV(tick N) = 0.0 ?
		// Note: AV=Actual Velocity, PV=Predicted Velocity
		if !(iFrameInfo.ActualVelocity[0] == 0.0 &&
			iFrameInfo.ActualVelocity[1] == 0.0 &&
			encoder.PlayerFramesMap[player.Name][lastIdx].ActualVelocity[0] == 0.0 &&
			encoder.PlayerFramesMap[player.Name][lastIdx].ActualVelocity[1] == 0.0) {
			var velAngle float64 = 0.0
			if iFrameInfo.ActualVelocity[0] == 0.0 {
				if iFrameInfo.ActualVelocity[1] < 0.0 {
					velAngle = 270.0
				} else {
					velAngle = 90.0
				}
			} else {
				velAngle = radian2degree(math.Atan2(float64(iFrameInfo.ActualVelocity[1]), float64(iFrameInfo.ActualVelocity[0])))
			}
			faceFront := normalizeDegree(float64(iFrameInfo.PredictedAngles[1]))
			deltaAngle := normalizeDegree(velAngle - faceFront)

			const threshold = 30.0
			if 0.0+threshold < deltaAngle && deltaAngle < 180.0-threshold {
				_preVel[1] = -450.0 // left
			}
			if 90.0+threshold < deltaAngle && deltaAngle < 270.0-threshold {
				_preVel[0] = -450.0 // back
			}
			if 180.0+threshold < deltaAngle && deltaAngle < 360.0-threshold {
				_preVel[1] = 450.0 // right
			}
			if 270.0+threshold < deltaAngle || deltaAngle < 90.0-threshold {
				_preVel[0] = 450.0 // front
			}
		}
	}

	encoder.PlayerFramesMap[player.Name] = append(encoder.PlayerFramesMap[player.Name], *iFrameInfo)
}

func saveToRecFile(player *common.Player, roundNum int32) {
	teamSuffix := "t"
	if player.Team == common.TeamCounterTerrorists {
		teamSuffix = "ct"
	}

	encoder.WriteToRecFile(player.Name, roundNum, teamSuffix)
}

// 插值相关函数
// 线性插值函数
func lerp(a, b, t float32) float32 {
	return a + (b-a)*t
}

// Catmull-Rom 样条插值
func catmullRom(p0, p1, p2, p3, t float32) float32 {
	t2 := t * t
	t3 := t2 * t

	return 0.5 * ((2.0 * p1) +
		(-p0+p2)*t +
		(2.0*p0-5.0*p1+4.0*p2-p3)*t2 +
		(-p0+3.0*p1-3.0*p2+p3)*t3)
}

// 角度插值（处理360度环绕问题）
func lerpAngle(a, b, t float32) float32 {
	// 计算最短角度差
	diff := b - a

	// 处理角度环绕（-180 到 180）
	if diff > 180 {
		diff -= 360
	} else if diff < -180 {
		diff += 360
	}

	result := a + diff*t

	// 标准化到 [-180, 180] 范围
	for result > 180 {
		result -= 360
	}
	for result < -180 {
		result += 360
	}

	return result
}

func interpolatePlayerFrames(playerName string) {
	frames := encoder.PlayerFramesMap[playerName]
	if len(frames) <= 1 {
		return
	}

	originalCount := len(frames)
	targetFPS := 128.0
	currentFPS := detectedFrameRate

	// 计算需要插入多少帧
	interpolationFactor := int(targetFPS / currentFPS)
	if interpolationFactor <= 1 {
		return // 不需要插帧
	}

	var interpolatedFrames []encoder.FrameInfo

	// 对每两帧之间进行插值
	for i := 0; i < len(frames)-1; i++ {
		currentFrame := frames[i]
		nextFrame := frames[i+1]

		// 添加当前帧
		interpolatedFrames = append(interpolatedFrames, currentFrame)

		// 获取前后帧用于更平滑的插值
		var prevFrame, nextNextFrame encoder.FrameInfo
		if i > 0 {
			prevFrame = frames[i-1]
		} else {
			prevFrame = currentFrame
		}
		if i < len(frames)-2 {
			nextNextFrame = frames[i+2]
		} else {
			nextNextFrame = nextFrame
		}

		// 在当前帧和下一帧之间插入 (interpolationFactor - 1) 个帧
		for j := 1; j < interpolationFactor; j++ {
			t := float32(j) / float32(interpolationFactor)

			midFrame := encoder.FrameInfo{}

			// 使用 Catmull-Rom 样条插值位置（更平滑）
			for k := 0; k < 3; k++ {
				midFrame.Origin[k] = catmullRom(
					prevFrame.Origin[k],
					currentFrame.Origin[k],
					nextFrame.Origin[k],
					nextNextFrame.Origin[k],
					t,
				)
			}

			// 速度使用线性插值（速度变化本来就快）
			for k := 0; k < 3; k++ {
				midFrame.ActualVelocity[k] = lerp(currentFrame.ActualVelocity[k], nextFrame.ActualVelocity[k], t)
				midFrame.PredictedVelocity[k] = lerp(currentFrame.PredictedVelocity[k], nextFrame.PredictedVelocity[k], t)
			}

			// 角度使用专门的角度插值（处理360度环绕）
			midFrame.PredictedAngles[0] = lerpAngle(currentFrame.PredictedAngles[0], nextFrame.PredictedAngles[0], t)
			midFrame.PredictedAngles[1] = lerpAngle(currentFrame.PredictedAngles[1], nextFrame.PredictedAngles[1], t)

			// 按钮状态：使用当前帧的按钮（不插值）
			midFrame.PlayerButtons = currentFrame.PlayerButtons
			midFrame.PlayerImpulse = currentFrame.PlayerImpulse
			midFrame.PlayerSeed = currentFrame.PlayerSeed
			midFrame.PlayerSubtype = currentFrame.PlayerSubtype

			// 武器切换：只在原始帧保留，插值帧设为NONE
			midFrame.CSWeaponID = int32(CSWeapon_NONE)

			// 处理附加字段
			midFrame.AdditionalFields = 0

			// 如果两帧都有Origin附加字段，进行插值
			if currentFrame.AdditionalFields&encoder.FIELDS_ORIGIN != 0 &&
				nextFrame.AdditionalFields&encoder.FIELDS_ORIGIN != 0 {
				midFrame.AdditionalFields |= encoder.FIELDS_ORIGIN
				for k := 0; k < 3; k++ {
					midFrame.AtOrigin[k] = catmullRom(
						prevFrame.AtOrigin[k],
						currentFrame.AtOrigin[k],
						nextFrame.AtOrigin[k],
						nextNextFrame.AtOrigin[k],
						t,
					)
				}
			}

			// 如果两帧都有Velocity附加字段，进行插值
			if currentFrame.AdditionalFields&encoder.FIELDS_VELOCITY != 0 &&
				nextFrame.AdditionalFields&encoder.FIELDS_VELOCITY != 0 {
				midFrame.AdditionalFields |= encoder.FIELDS_VELOCITY
				for k := 0; k < 3; k++ {
					midFrame.AtVelocity[k] = lerp(currentFrame.AtVelocity[k], nextFrame.AtVelocity[k], t)
				}
			}

			interpolatedFrames = append(interpolatedFrames, midFrame)
		}
	}

	// 添加最后一帧
	interpolatedFrames = append(interpolatedFrames, frames[len(frames)-1])

	newCount := len(interpolatedFrames)
	encoder.PlayerFramesMap[playerName] = interpolatedFrames

	ilog.InfoLogger.Printf("    [插帧] %s: %d帧 → %d帧 (%.1fx)",
		playerName, originalCount, newCount, float64(newCount)/float64(originalCount))
}
