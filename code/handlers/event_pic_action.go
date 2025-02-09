package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"image"
	"image/png"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"start-feishubot/initialization"
	"start-feishubot/logger"
	"start-feishubot/services/openai"
	"strings"
	"time"
)

type PicAction struct { /*图片*/
}

func (*PicAction) Execute(a *ActionInfo) bool {
	logger.Warn("PicAction Execute!!!")

	if a.info.msgType == "image" {
		// read url
		url, err := readUrl()
		if err != nil {
			logger.Info("read painter url failed")
			return false
		}
		if url == "" {
			replyMsg(*a.ctx, "AI绘图任务功能关闭了，改天再来吧", a.info.msgId)
			return false
		}
		logger.Info(fmt.Sprintf("sd_painter_url: %s", url))

		//保存图片
		imageKey := a.info.imageKey
		//fmt.Printf("fileKey: %s \n", imageKey)
		msgId := a.info.msgId
		//fmt.Println("msgId: ", *msgId)
		req := larkim.NewGetMessageResourceReqBuilder().MessageId(
			*msgId).FileKey(imageKey).Type("image").Build()
		resp, err := initialization.GetLarkClient().Im.MessageResource.Get(context.Background(), req)
		//fmt.Println(resp, err)
		if err != nil {
			//fmt.Println(err)
			replyMsg(*a.ctx, fmt.Sprintf("🤖️：图片下载失败，请稍后再试～\n 错误信息: %v", err),
				a.info.msgId)
			return false
		}

		logger.Warnf("filename: %s", imageKey)
		f_jpg := fmt.Sprintf("%s.jpg", imageKey)
		f_png := fmt.Sprintf("%s.png", imageKey)
		defer os.Remove(f_jpg)
		defer os.Remove(f_png)
		resp.WriteFile(f_jpg)

		// 检查是否已经是png
		is_png, err := openai.IsPngs([]string{f_jpg})
		if !is_png {
			err := openai.ConvertJpegToPNG(f_jpg)
			if err != nil {
				logger.Warnf("ConvertJpegToPNG error: %s", err)
				replyMsg(*a.ctx, fmt.Sprintf("🤖️：无法转为png格式: %s", err), a.info.msgId)
				return false
			}
		} else if err != nil {
			logger.Warnf("VerifyPngs error: %s", err)
			replyMsg(*a.ctx, fmt.Sprintf("🤖️：无法解析图片: %s", err), a.info.msgId)
			return false
		} else {
			os.Rename(f_jpg, f_png)
		}

		openai.ConvertToRGBA(f_png, f_png)

		//图片校验
		is_png, err = openai.IsPngs([]string{f_png})
		if err != nil || !is_png {
			logger.Warnf("VerifyPngs again error: %s", err)
			replyMsg(*a.ctx, fmt.Sprintf("🤖️：无法解析图片: %s", err), a.info.msgId)
			return false
		}

		// send task
		taskId, err := sendTask(url, f_png)
		if err != nil {
			replyMsg(*a.ctx, fmt.Sprintf("AI绘图任务发送出错: %s", err), a.info.msgId)
			logger.Warn(fmt.Sprintf("AI绘图任务发送出错: %s", err))
			return false
		}
		replyMsg(*a.ctx, fmt.Sprintf("AI绘图任务发送成功，任务ID: %s", taskId), a.info.msgId)

		// get result
		data := []byte(fmt.Sprintf(`{"task_id": "%s"}`, taskId))
		for {
			time.Sleep(10 * time.Second)
			resp, err := http.Post(url+"/get_task_result", "application/json", bytes.NewBuffer(data))
			if err != nil {
				replyMsg(*a.ctx, fmt.Sprintf("任务ID: %s\n获取结果出错", taskId), a.info.msgId)
				logger.Warn(fmt.Sprintf("任务ID: %s\n获取结果出错", taskId))
				return false
			}
			defer resp.Body.Close()

			var taskResult struct {
				Result string `json:"result"`
			}
			err = json.NewDecoder(resp.Body).Decode(&taskResult)
			if err != nil {
				replyMsg(*a.ctx, fmt.Sprintf("任务ID: %s\n解析结果出错", taskId), a.info.msgId)
				logger.Warn(fmt.Sprintf("任务ID: %s\n解析结果出错", taskId))
				return false
			}

			result := taskResult.Result
			if strings.HasPrefix(result, "process") {
				replyMsg(*a.ctx, fmt.Sprintf("任务ID: %s\n正在绘图...", taskId), a.info.msgId)
			} else if strings.HasPrefix(result, "in queue") {
				seq := strings.Replace(result, "in queue", "", 1)
				replyMsg(*a.ctx, fmt.Sprintf("任务ID: %s\n正在排队: %s", taskId, seq), a.info.msgId)
			} else {
				replyMsg(*a.ctx, fmt.Sprintf("任务ID: %s\n绘图完成正在发送...", taskId), a.info.msgId)
				logger.Info("cpmplete task_id: %s", taskId)

				replayImagePlainByBase64(*a.ctx, result, a.info.msgId)

				img, err := decodeImage(result)
				if err != nil {
					logger.Info("save failed task_id: %s", taskId)
					return false
				}
				saveImage(img, fmt.Sprintf("%s.png", taskId), "outputs")
				logger.Info("save task_id: %s", taskId)

				return false
			}
		}
	}

	return true
}

func encodeImage(path string) (string, int, int, error) {
	srcbytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", 0, 0, err
	}

	file, _ := os.Open(path)
	c, _, _ := image.DecodeConfig(file)

	encodedImage := base64.StdEncoding.EncodeToString(srcbytes)
	return encodedImage, c.Width, c.Height, nil
}

func decodeImage(encodedImage string) (image.Image, error) {
	data, err := base64.StdEncoding.DecodeString(encodedImage)
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	return img, nil
}

func saveImage(img image.Image, fileName string, saveDir string) error {
	os.Mkdir(saveDir, 0755)
	savePath := filepath.Join(saveDir, fileName)
	file, err := os.Create(savePath)
	if err != nil {
		return err
	}
	defer file.Close()

	err = png.Encode(file, img)
	if err != nil {
		return err
	}
	return nil
}

func sendTask(url string, filePath string) (string, error) {
	encodedImage, width, height, err := encodeImage(filePath)
	if err != nil {
		return "", err
	}

	// send task
	resp, err := http.Post(url+"/insert_task", "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"mode": "repaint", "style": "anime", "encoded_image": "%s", "width": %d, "height": %d}`, encodedImage, width, height))))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var taskId struct {
		TaskId string `json:"task_id"`
	}

	err = json.NewDecoder(resp.Body).Decode(&taskId)
	if err != nil {
		return "", err
	}

	logger.Info("task_id: %s", taskId.TaskId)
	return taskId.TaskId, nil
}

func readUrl() (string, error) {
	data, err := ioutil.ReadFile("sd_painter_url")
	if err != nil {
		return "", err
	}
	return strings.Replace(string(data), "\n", "", -1), nil
}
