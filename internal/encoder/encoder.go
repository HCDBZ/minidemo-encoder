package encoder

import (
	"bytes"
	"fmt"
	"os"
	"time"

	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
)

const __MAGIC__ int32 = -559038737
const __FORMAT_VERSION__ int8 = 2
const FIELDS_ORIGIN int32 = 1 << 0
const FIELDS_ANGLES int32 = 1 << 1
const FIELDS_VELOCITY int32 = 1 << 2

var bufMap map[string]*bytes.Buffer = make(map[string]*bytes.Buffer)
var PlayerFramesMap map[string][]FrameInfo = make(map[string][]FrameInfo)

var saveDir string = "./output"

func init() {
	if ok, _ := PathExists(saveDir); !ok {
		os.Mkdir(saveDir, os.ModePerm)
		ilog.InfoLogger.Println("创建输出目录：", saveDir)
	}
}

// SetSaveDir 设置输出目录
func SetSaveDir(dir string) {
	saveDir = dir
	// 确保目录存在
	if ok, _ := PathExists(saveDir); !ok {
		err := os.MkdirAll(saveDir, os.ModePerm)
		if err != nil {
			ilog.ErrorLogger.Println("创建输出目录失败:", err.Error())
		} else {
			ilog.InfoLogger.Println("创建输出目录：", saveDir)
		}
	}
}

func InitPlayer(initFrame FrameInitInfo) {
	if bufMap[initFrame.PlayerName] == nil {
		bufMap[initFrame.PlayerName] = new(bytes.Buffer)
	} else {
		bufMap[initFrame.PlayerName].Reset()
	}
	// step.1 MAGIC NUMBER
	WriteToBuf(initFrame.PlayerName, __MAGIC__)

	// step.2 VERSION
	WriteToBuf(initFrame.PlayerName, __FORMAT_VERSION__)

	// step.3 timestamp
	WriteToBuf(initFrame.PlayerName, int32(time.Now().Unix()))

	// step.4 name length
	WriteToBuf(initFrame.PlayerName, int8(len(initFrame.PlayerName)))

	// step.5 name
	WriteToBuf(initFrame.PlayerName, []byte(initFrame.PlayerName))

	// step.6 initial position
	for idx := 0; idx < 3; idx++ {
		WriteToBuf(initFrame.PlayerName, float32(initFrame.Position[idx]))
	}

	// step.7 initial angle
	for idx := 0; idx < 2; idx++ {
		WriteToBuf(initFrame.PlayerName, initFrame.Angles[idx])
	}
}

func WriteToRecFile(playerName string, roundNum int32, teamSide string) {
	// 新的目录结构：output/demo名称/round1/t/ 或 output/demo名称/round1/ct/
	roundDir := fmt.Sprintf("%s/round%d", saveDir, roundNum)
	teamDir := fmt.Sprintf("%s/%s", roundDir, teamSide)

	// 确保目录存在
	if ok, _ := PathExists(teamDir); !ok {
		err := os.MkdirAll(teamDir, os.ModePerm)
		if err != nil {
			ilog.ErrorLogger.Println("创建目录失败:", err.Error())
			return
		}
	}

	fileName := fmt.Sprintf("%s/%s.rec", teamDir, playerName)
	file, err := os.Create(fileName)
	if err != nil {
		ilog.ErrorLogger.Println("文件创建失败:", err.Error())
		return
	}
	defer file.Close()

	// step.8 tick count
	var tickCount int32 = int32(len(PlayerFramesMap[playerName]))
	WriteToBuf(playerName, tickCount)

	// step.9 bookmark count
	WriteToBuf(playerName, int32(0))

	// step.10 all bookmark
	// ignore

	// step.11 all tick frame
	for _, frame := range PlayerFramesMap[playerName] {
		WriteToBuf(playerName, frame.PlayerButtons)
		WriteToBuf(playerName, frame.PlayerImpulse)

		// ActualVelocity (3 floats)
		for idx := 0; idx < 3; idx++ {
			WriteToBuf(playerName, frame.ActualVelocity[idx])
		}

		// PredictedVelocity (3 floats)
		for idx := 0; idx < 3; idx++ {
			WriteToBuf(playerName, frame.PredictedVelocity[idx])
		}

		// PredictedAngles (2 floats)
		for idx := 0; idx < 2; idx++ {
			WriteToBuf(playerName, frame.PredictedAngles[idx])
		}

		// Origin (3 floats)
		for idx := 0; idx < 3; idx++ {
			WriteToBuf(playerName, frame.Origin[idx])
		}

		WriteToBuf(playerName, frame.CSWeaponID)
		WriteToBuf(playerName, frame.PlayerSubtype)
		WriteToBuf(playerName, frame.PlayerSeed)
		WriteToBuf(playerName, frame.AdditionalFields)

		// 附加信息
		if frame.AdditionalFields&FIELDS_ORIGIN != 0 {
			for idx := 0; idx < 3; idx++ {
				WriteToBuf(playerName, frame.AtOrigin[idx])
			}
		}
		if frame.AdditionalFields&FIELDS_ANGLES != 0 {
			for idx := 0; idx < 3; idx++ {
				WriteToBuf(playerName, frame.AtAngles[idx])
			}
		}
		if frame.AdditionalFields&FIELDS_VELOCITY != 0 {
			for idx := 0; idx < 3; idx++ {
				WriteToBuf(playerName, frame.AtVelocity[idx])
			}
		}
	}

	// 写入文件
	_, writeErr := file.Write(bufMap[playerName].Bytes())
	if writeErr != nil {
		ilog.ErrorLogger.Printf("写入文件失败 [%s]: %s\n", fileName, writeErr.Error())
		return
	}

	// 清理内存
	delete(PlayerFramesMap, playerName)

	// 输出更简洁的日志
	teamName := "T"
	if teamSide == "ct" {
		teamName = "CT"
	}
	ilog.InfoLogger.Printf("    ✓ %s (%s) - %d 帧", playerName, teamName, tickCount)
}
