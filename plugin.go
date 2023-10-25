package main

import (
	"errors"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/gorilla/websocket"
	"log"
	"math/rand"
	"regexp"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gotify/plugin-api"
)

// GetGotifyPluginInfo 返回 Gotify 插件信息
func GetGotifyPluginInfo() plugin.Info {
	return plugin.Info{
		Name:       "ESPDeerAdapter",
		ModulePath: "https://github.com/AmamiyaHotaru/GotifyToESPDeerAdapter",
		Author:     "amamiyahotaru",
	}
}

type Server struct {
	MQTTServer  string
	Username    string
	Password    string
	ClientID    string
	HostServer  string
	ClientToken string
	MQTTTopic   string
}

type Config struct {
	Server *Server
}

// Plugin 是插件实例
type Plugin struct {
	userCtx       plugin.UserContext
	msgHandler    plugin.MessageHandler
	config        *Config
	client        mqtt.Client
	enabled       bool
	websocketConn *websocket.Conn
}

// Message websocketMessage实例
type Message struct {
	Message  string  `json:"message"`
	Priority int     `json:"priority"`
	Title    string  `json:"title"`
	Extras   *Extras `json:"extras"`
}

type Extras struct {
	ClientDisplay *ClientDisplay `json:"client::display"`
}

type ClientDisplay struct {
	ContentType string `json:"contentType"`
}

// SetMessageHandler 实现 plugin.Messenger
// 在初始化期间调用
func (p *Plugin) SetMessageHandler(h plugin.MessageHandler) {
	p.msgHandler = h
}

// Enable 向上下文映射添加用户
func (p *Plugin) Enable() error {
	if len(p.config.Server.HostServer) < 1 {
		return errors.New("请输入正确的 Web 服务器")
	} else {
		// 检查 token 是否存在
		if len(p.config.Server.ClientToken) < 1 { // 如果为空
			return errors.New("请先添加客户端令牌")
		}

		// 创建 URL
		myurl := p.config.Server.HostServer + "/stream?token=" + p.config.Server.ClientToken
		// 检查它是否有效

		err := p.TestSocket(myurl)
		if err != nil {
			return errors.New("web 服务器 URL 无效，无效的客户端令牌或 URL")
		}
		// 连接WebSocket
		go p.connectWebSocket(myurl)

		// 连接MQTT
		go p.connectMQTT()

		// 定时执行发送消息和检测连接的任务
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			reconnectTicker := time.NewTicker(30 * time.Second) // 添加一个重新连接的定时器
			for {
				select {
				case <-ticker.C:
					// 每隔 10 秒发送一条消息以保持连接
					p.sendMessageToWebSocket("THUMP!")

				case <-reconnectTicker.C:
					// 定期检测连接状态并重新连接
					p.checkAndReconnect()
				}
			}
		}()
	}
	p.enabled = true
	return nil
}

func (p *Plugin) checkAndReconnect() error {
	// 检查 WebSocket 连接
	if p.websocketConn == nil {
		log.Println("WebSocket 连接未建立，尝试重新连接...")
		// 创建 URL
		myurl := p.config.Server.HostServer + "/stream?token=" + p.config.Server.ClientToken
		// 检查它是否有效

		err := p.TestSocket(myurl)
		if err != nil {
			return errors.New("web 服务器 URL 无效，无效的客户端令牌或 URL")
		}
		// 连接WebSocket
		go p.connectWebSocket(myurl)
		log.Println("WebSocket，重新连接成功")
	}

	// 检查 MQTT 连接
	if p.client == nil || !p.client.IsConnected() {
		log.Println("MQTT 客户端未连接，尝试重新连接...")
		p.connectMQTT()
		log.Println("MQTT，重新连接成功")
	}
	return nil
}

func (p *Plugin) connectMQTT() {
	// 连接到MQTT服务器
	opts := mqtt.NewClientOptions()

	serverConfig := p.config.Server

	opts.AddBroker(serverConfig.MQTTServer)
	opts.SetClientID(serverConfig.ClientID)

	if serverConfig.Username != "" {
		opts.SetUsername(serverConfig.Username)
	}

	if serverConfig.Password != "" {
		opts.SetPassword(serverConfig.Password)
	}

	client := mqtt.NewClient(opts)

	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("连接到 MQTT 服务器错误: %v", token.Error())
		return
	}

	p.client = client

}

func (p *Plugin) connectWebSocket(myURL string) {
	// 连接到WebSocket并监听消息
	conn, _, err := websocket.DefaultDialer.Dial(myURL, nil)
	if err != nil {
		log.Printf("连接到 WebSocket 错误: %v", err)
		time.Sleep(time.Second * 5) // 重试连接
		return
	}
	p.websocketConn = conn

	// 循环监听WebSocket消息
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket 消息读取错误: %v", err)
			break
		}
		// 打印消息的值
		log.Println(string(msg))
		var message Message
		if err := json.Unmarshal(msg, &message); err != nil {
			fmt.Printf("JSON 解码错误: %v\n", err)
			return
		}
		var mqttMessage string
		urlRegex := regexp.MustCompile(`!\[.*\]\(([^)]+)\)`)
		mqttTopic := p.config.Server.MQTTTopic
		mqttMessage = "【" + message.Title + "】" + message.Message
		if message.Extras != nil && len(message.Extras.ClientDisplay.ContentType) != 0 && message.Extras.ClientDisplay.ContentType == "text/markdown" {
			urlMatch := urlRegex.FindStringSubmatch(message.Message)
			if len(urlMatch) > 1 {
				mqttTopic += "_bg_url"
				mqttMessage = urlMatch[1]
			}
		} else {
			mqttTopic += "_text"
		}
		log.Printf("推送的message是" + mqttMessage + "-----" + "topic是" + mqttTopic)
		// 将WebSocket消息发送到MQTT
		p.publishToMQTT(mqttMessage, mqttTopic)
	}
}

func (p *Plugin) publishToMQTT(message string, topic string) {
	if p.client == nil || !p.client.IsConnected() {
		log.Println("MQTT 客户端未连接，无法发布消息。")
		return
	}

	token := p.client.Publish(topic, 0, false, message)
	if token.Wait() && token.Error() != nil {
		log.Printf("MQTT 消息发布错误: %v", token.Error())
	}
}

func (p *Plugin) sendMessageToWebSocket(message string) {
	if p.websocketConn == nil {
		log.Println("WebSocket 连接未建立，无法发送消息。")
		return
	}

	err := p.websocketConn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		log.Printf("WebSocket 发送消息错误: %v", err)
	}
	log.Printf("THUMP")

}

func (p *Plugin) TestSocket(myurl string) (err error) {
	_, _, err = websocket.DefaultDialer.Dial(myurl, nil)
	if err != nil {
		log.Println("测试拨号错误: ", err)
		return err
	}
	log.Println("成功连接到 WebSocket")
	return nil
}

// Disable 从上下文映射中移除用户
func (p *Plugin) Disable() error {
	p.enabled = false
	p.disconnectClients()
	return nil
}

// DefaultConfig 实现 plugin.Configurer
func (p *Plugin) DefaultConfig() interface{} {
	return &Config{
		Server: &Server{MQTTServer: "127.0.0.1:1883", MQTTTopic: "*", HostServer: "ws://localhost:8000"},
	}
}

// ValidateAndSetConfig 每次插件被初始化或配置被用户更改时都会被调用
func (p *Plugin) ValidateAndSetConfig(c interface{}) error {
	config := c.(*Config)

	// 如果监听器已配置，则关闭它们并重新启动
	p.disconnectClients()

	if config.Server.MQTTServer == "" {
		return errors.New("无效地址")
	}
	if config.Server.ClientID == "" {
		config.Server.ClientID = "gotify-" + randString()
	}

	p.config = config

	// 如果已启用并且配置已更新，则重新连接客户端
	if p.enabled {
		p.disconnectClients()
		if len(p.config.Server.HostServer) < 1 {
			return errors.New("请输入正确的 Web 服务器")
		} else {
			// 检查 token 是否存在
			if len(p.config.Server.ClientToken) < 1 { // 如果为空
				return errors.New("请先添加客户端令牌")
			}

			// 创建 URL
			myurl := p.config.Server.HostServer + "/stream?token=" + p.config.Server.ClientToken
			// 检查它是否有效

			err := p.TestSocket(myurl)
			if err != nil {
				return errors.New("web 服务器 URL 无效，无效的客户端令牌或 URL")
			}
			// 连接WebSocket
			go p.connectWebSocket(myurl)

			// 连接MQTT
			go p.connectMQTT()
		}
	}

	return nil
}

func (p *Plugin) disconnectClients() {
	if p.client != nil {
		if p.client.IsConnected() {
			p.client.Disconnect(500) // 断开 MQTT 连接
		}
		p.client = nil
	}

	if p.websocketConn != nil {
		p.websocketConn.Close() // 关闭 WebSocket 连接
		p.websocketConn = nil
	}
}

func randString() string {
	letterRunes := []rune("0123456789abcdef")
	b := make([]rune, 16)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

// NewGotifyPluginInstance 为用户上下文创建插件实例
func NewGotifyPluginInstance(ctx plugin.UserContext) plugin.Plugin {
	rand.Seed(time.Now().UnixNano())
	return &Plugin{
		userCtx: ctx,
	}
}

func main() {
	panic("程序必须编译为 Go 插件")
}
