package tty

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/lflxp/showme/utils"
	log "github.com/sirupsen/logrus"
)

// 服务端内部处理对象
type ClientContext struct {
	Xtermjs *XtermJs        // 前端配置
	Request *http.Request   // http客户端请求
	WsConn  *websocket.Conn // websocket连接
	Cmd     *exec.Cmd       // exec.Command实例
	Pty     *os.File        // 命令行pty代理
	// Cache      *bytes.Buffer   // 命令缓存
	// CacheMutex *sync.Mutex     // 缓存并发锁
	WriteMutex *sync.Mutex // 并发安全 通过ws发送给客户
}

// 处理请求
// 三个go 无阻赛
func (this *ClientContext) HandleClient() {
	// 创建退出channel
	exit := make(chan bool, 2)

	// 处理发送请求
	go func() {
		this.Send(exit)
	}()
	// 处理接受请求（执行命令）
	go func() {
		this.Receive(exit)
	}()
	// 处理退出请求
	go func() {
		// 结束异步请求
		defer this.Xtermjs.Server.DoneGo()
		defer func() {
			conns := atomic.AddInt64(this.Xtermjs.Connections, -1)
			if this.Xtermjs.Options.MaxConnections != 0 {
				log.Infof("连接关闭: %s , 连接状态: %d/%d", this.Request.RemoteAddr, conns, this.Xtermjs.Options.MaxConnections)
			} else {
				log.Infof("连接关闭: %s, 连接总数: %d", this.Request.RemoteAddr, conns)
			}
		}()

		// 任意接受或发送被关闭 立即触发
		<-exit
		this.Pty.Close()

		// Even if the PTY has been closed,
		// Read(0 in processSend() keeps blocking and the process doen't exit
		this.Cmd.Process.Signal(syscall.Signal(this.Xtermjs.Options.CloseSignal))

		this.Cmd.Wait()
		this.WsConn.Close()
	}()
}

func setQuit(quit chan bool) {
	quit <- true
}

// 发送命令的执行结果
// 不执行具体任务
func (this *ClientContext) Send(quitChan chan bool) {
	defer setQuit(quitChan)

	buf := make([]byte, 1024)

	for {
		select {
		case <-quitChan:
			log.Info("Close Send Channel")
			return
		default:
			// 读取命令执行结果并通过ws反馈给用户
			size, err := this.Pty.Read(buf)
			if err != nil {
				log.Warnf("Send[87] %s -> %s", this.Request.RemoteAddr, err.Error())
				return
			}
			log.Debugf("Send Size: %d\n", size)
			// 将所有返回结果包括UTF8编码的内容用base64进行编码，client解码再显示，避免了直接UTF8编码传输的报错
			// Could not decode a text frame as UTF-8 的解决
			safeMessage := base64.StdEncoding.EncodeToString([]byte(buf[:size]))
			if err = this.write([]byte(safeMessage)); err != nil {
				log.Error(err.Error())
				return
			}
		}
	}
}

// xsrf验证
// token = xsrf + request.remoteAddr
func (this *ClientContext) ParseXsrf(info []byte) (string, string, bool) {
	token := fmt.Sprintf("%s%s", string(info[1:33]), strings.Split(this.Request.RemoteAddr, ":")[0])
	if v, ok := this.Xtermjs.XsrfToken.Load(token); ok {
		log.Debugf("%s XsrfToken %s Created %s Message %s", this.Request.RemoteAddr, string(info[1:33]), v.(string), string(info[33:]))
		return token, string(info[33:]), true
	}
	return token, string(info[33:]), false
}

// 获取用户发送命令
// 发送到pty进行执行
func (this *ClientContext) Receive(quitChan chan bool) {
	defer setQuit(quitChan)
	for {
		select {
		case <-quitChan:
			log.Info("Close Recive Channel")
			return
		default:
			// 读取ws中的数据
			_, message, err := this.WsConn.ReadMessage()
			if err != nil {
				log.Warnf("Receive[112] %s", err.Error())
				return
			}

			if len(message) == 0 {
				log.Error("An error mesaage length is 0")
				return
			}

			log.Debugf("Receive %s\n", string(message))

			// Xsrf校验
			cacheKey, msg, status := this.ParseXsrf(message)
			if !status {
				tmp := &Aduit{
					Remoteaddr: this.Request.RemoteAddr,
					Token:      string(message[1:33]),
					Command:    msg,
					Pid:        this.Cmd.Process.Pid,
					Status:     "非法xsrf",
				}
				err = AddAduit(tmp)
				if err != nil {
					log.Error("AddAduit error", err.Error())
				}
				this.write([]byte("\x1B[1;3;31mPermission Denied\x1B[0m\n"))
				break
			}
			defer xterm.XsrfToken.Delete(cacheKey)

			// 利用cache来计算命令，并写入数据库
			// remoteAddr cmd 入库字段

			// go func() {
			// 	if message[0] == Input {
			// 		rs, err := utils.DecodeBase64(msg)
			// 		if err != nil {
			// 			log.Error("Recive[129]", err.Error())
			// 			return
			// 		}
			// 		switch rs {
			// 		case "\r\n":
			// 			log.Debug("Command %s", this.Cache.String())
			// 			// 清楚上一次的缓存命令
			// 			// TODO insert into databases
			// 			this.Cache.Reset()
			// 		default:
			// 			this.cacheWrite([]byte(rs))
			// 		}
			// 	}
			// }()

			// 审计
			if this.Xtermjs.Options.Audit {
				cm, err := utils.DecodeBase64(msg)
				if err != nil {
					log.Errorf("Recive[172] [%s] %s", msg, err.Error())
					break
				}
				tmp := &Aduit{
					Remoteaddr: this.Request.RemoteAddr,
					Token:      string(message[1:33]),
					Command:    cm,
					Pid:        this.Cmd.Process.Pid,
					Status:     "success",
				}
				err = AddAduit(tmp)
				if err != nil {
					log.Error("AddAduit error", err.Error())
				}
			}

			// 判断命令
			// @Params msg:xsrf:message  0:32:&
			switch message[0] {
			case Input:
				// 用户是否能写入
				if !this.Xtermjs.Options.PermitWrite {
					break
				}

				// base64解码
				decode, err := utils.DecodeBase64Bytes(msg)
				if err != nil {
					log.Error("Recive[156] ", err.Error())
					break
				}

				// 向pty中传入执行命令
				_, err = this.Pty.Write(decode)
				if err != nil {
					log.Error("Recive[163] ", err.Error())
					return
				}
			case Heartbeat:
				this.write([]byte(""))
			case Ping:
				this.write([]byte("pong"))
			case ResizeTerminal:
				// @数据格式 type+rows:cols
				// base64解码
				decode, err := utils.DecodeBase64(msg)
				if err != nil {
					log.Errorf("Recive[175] %s", err.Error())
					break
				}

				tmp := strings.Split(decode, ":")
				rows, err := strconv.Atoi(tmp[0])
				if err != nil {
					log.Errorf("Recive[182] %s", err.Error())
					this.write([]byte(err.Error()))
					break
				}
				cols, err := strconv.Atoi(tmp[1])
				if err != nil {
					log.Errorf("Recive[188] %s", err.Error())
					this.write([]byte(err.Error()))
					break
				}
				window := struct {
					row uint16
					col uint16
					x   uint16
					y   uint16
				}{
					uint16(rows),
					uint16(cols),
					0,
					0,
				}
				syscall.Syscall(
					syscall.SYS_IOCTL,
					this.Pty.Fd(),
					syscall.TIOCSWINSZ,
					uintptr(unsafe.Pointer(&window)),
				)
			default:
				this.write([]byte(fmt.Sprintf("Unknow Message Type %s", string(message[0]))))
				log.Error("Unknow Message Type %v", message[0])
			}
		}
	}
}

// 缓存并发安全
// func (this *ClientContext) cacheWrite(data []byte) error {
// 	this.CacheMutex.Lock()
// 	defer this.CacheMutex.Unlock()
// 	_, err := this.Cache.Write(data)
// 	return err
// }

// 发送websocket信息给客户端
func (this *ClientContext) write(data []byte) error {
	this.WriteMutex.Lock()
	defer this.WriteMutex.Unlock()
	return this.WsConn.WriteMessage(websocket.TextMessage, data)
}
