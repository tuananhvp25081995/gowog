package game

// package game is reponsible for gamelogic and network.
// For each event it received, it will do game logic and sending message to other.

import (
	"fmt"
	"math"
	"time"

	"github.com/giongto35/gowog/server/game/common"
	"github.com/giongto35/gowog/server/game/mappkg"
	"github.com/giongto35/gowog/server/game/objmanager"
	"github.com/giongto35/gowog/server/game/playerpkg"
	"github.com/giongto35/gowog/server/game/shape"
	"github.com/giongto35/gowog/server/game/shootpkg"

	"github.com/giongto35/gowog/server/Message_proto"
	"github.com/giongto35/gowog/server/game/gameconst"
	"github.com/giongto35/gowog/server/game/ws"
	"github.com/golang/protobuf/proto"
)

type gameImpl struct {
	hub                 ws.Hub
	destroyPlayerStream chan common.DestroyPlayerEvent
	newPlayerStream     chan common.NewPlayerEvent
	inputStream         chan common.ProcessInputEvent
	objManager          objmanager.ObjectManager
	quitChannel         chan bool
}

// NewGame create new game master
func NewGame(hub ws.Hub) Game {
	var g = gameImpl{}
	g.hub = hub

	// Setup Object manager
	g.destroyPlayerStream = make(chan common.DestroyPlayerEvent, gameconst.BufferSize)
	g.newPlayerStream = make(chan common.NewPlayerEvent, gameconst.BufferSize)
	g.inputStream = make(chan common.ProcessInputEvent, gameconst.BufferSize)
	gameMap := mappkg.NewMap(gameconst.BlockWidth, gameconst.BlockHeight)
	g.objManager = objmanager.NewObjectManager(&g, g.destroyPlayerStream, gameMap)

	go hub.Run()
	g.quitChannel = g.gameUpdate()
	hub.BindGameMaster(&g)
	return &g
}

// gameUpdate is for game update, which update every a period
func (g *gameImpl) gameUpdate() (quit chan bool) {
	// Update loop
	ticker := time.NewTicker(gameconst.RefreshRate * time.Millisecond)

	quit = make(chan bool)
	go func() {
		for {
			select {
			case v := <-g.destroyPlayerStream:
				g.removePlayer(v.PlayerID, v.ClientID)

			case v := <-g.newPlayerStream:
				g.newPlayerConnect(v.Client)

			case v := <-g.inputStream:
				g.processInput(v.Message)

			case <-ticker.C:
				g.Update()

			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()

	return quit
}

// Update update all objects in each server ticks
func (g *gameImpl) Update() {

	// Update all object logic
	g.objManager.Update()

	// Send to all clients the updated environment
	for _, player := range g.objManager.GetPlayers() {
		updatePlayerMsg := &Message_proto.ServerGameMessage{
			Message: &Message_proto.ServerGameMessage_UpdatePlayerPayload{
				UpdatePlayerPayload: player.GetPlayerProto(),
			},
		}
		encodedMsg, _ := proto.Marshal(updatePlayerMsg)
		g.hub.Broadcast(encodedMsg)
	}
}

// Checking rect circle collision givent the shape
// TODO: Put this into different package
func (g *gameImpl) isCircleRectCollision(cir shape.Circle, rect shape.Rect) bool {
	if rect.X1 <= cir.X && cir.X <= rect.X2 && rect.Y1 <= cir.Y && cir.Y <= rect.Y2 {
		return true
	}
	var xnear = float32(math.Max(math.Min(float64(cir.X), float64(rect.X2)), float64(rect.X1)))
	var ynear = float32(math.Max(math.Min(float64(cir.Y), float64(rect.Y2)), float64(rect.Y1)))
	if g.dist(cir.X, cir.Y, xnear, ynear) <= cir.Radius {
		return true
	}
	return false
}

// dist calculate the distance between two points
func (g *gameImpl) dist(x1, y1, x2, y2 float32) float32 {
	return float32(math.Sqrt(math.Pow(float64(x2-x1), 2) + math.Pow(float64(y2-y1), 2)))
}

// ProcessInput receive the message from client and put it to queue
func (g *gameImpl) ProcessInput(message []byte) {
	g.inputStream <- common.ProcessInputEvent{Message: message}
}

// ProcessInput logic to process ProcessInputEvent messsage
func (g *gameImpl) processInput(message []byte) {
	msg := &Message_proto.ClientGameMessage{}
	_ = proto.Unmarshal(message, msg)
	// Process different type of message received from client
	switch msg.Message.(type) {
	case *Message_proto.ClientGameMessage_MovePositionPayload:
		fmt.Println("2", msg.GetMovePositionPayload().GetDx())
		// Move player game logic
		player, ok := g.objManager.GetPlayerByID(msg.GetMovePositionPayload().GetId())
		if !ok {
			break
		}
		g.objManager.MovePlayer(player, msg.GetMovePositionPayload().GetDx(), msg.GetMovePositionPayload().GetDy(), gameconst.PlayerSpeed, msg.GetTimeElapsed())
		// Update sequence number
		player.SetCurrentInputNumber(msg.InputSequenceNumber)

	case *Message_proto.ClientGameMessage_ShootPayload:
		player, ok := g.objManager.GetPlayerByID(msg.GetShootPayload().GetPlayerId())
		if !ok {
			break
		}
		if player.GetNextReload().After(time.Now()) {
			break
		}
		player.Shoot(msg.GetShootPayload().GetX(), msg.GetShootPayload().GetY(), msg.GetShootPayload().GetDx(), msg.GetShootPayload().GetDy())
		shoot := g.objManager.RegisterShoot(player, msg.GetShootPayload().GetX(), msg.GetShootPayload().GetY(), msg.GetShootPayload().GetDx(), msg.GetShootPayload().GetDy(), time.Now())
		g.sendShootMsg(shoot)

	case *Message_proto.ClientGameMessage_InitPlayerPayload:
		g.initPlayer(msg.GetInitPlayerPayload().GetClientId(), msg.GetInitPlayerPayload().GetName())

	case *Message_proto.ClientGameMessage_SetPositionPayload:
		// Set position player game logic
		player, ok := g.objManager.GetPlayerByID(msg.GetSetPositionPayload().GetId())
		if !ok {
			break
		}

		g.objManager.SetPlayerPosition(player, msg.GetSetPositionPayload().GetX(), msg.GetSetPositionPayload().GetY())
		// Update sequence number
		player.SetCurrentInputNumber(msg.InputSequenceNumber)

	}
}

// createInitAllMessage to create server formated message to initialize all the things
// This is the first message from server to clients for them to setup the whole environment
func (g *gameImpl) createInitAllMessage(players []playerpkg.Player, gameMap mappkg.Map) *Message_proto.ServerGameMessage {
	initAllMsg := &Message_proto.ServerGameMessage{}
	initPlayersMsg := []*Message_proto.InitPlayer{}
	initMapMsg := gameMap.ToProto()

	// Pack all the players
	for _, player := range g.objManager.GetPlayers() {
		initPlayer := &Message_proto.InitPlayer{
			Id:     player.GetID(),
			Name:   player.GetName(),
			X:      player.GetPosition().X,
			Y:      player.GetPosition().Y,
			IsMain: false,
		}
		initPlayersMsg = append(initPlayersMsg, initPlayer)
	}
	initAllMsg.Message = &Message_proto.ServerGameMessage_InitAllPayload{
		InitAllPayload: &Message_proto.InitAll{
			InitMap:    initMapMsg,
			InitPlayer: initPlayersMsg,
		},
	}
	return initAllMsg
}

// NewPlayerConnect is when new socket joins, we send all of the current player to it
// Put it into Channel
func (g *gameImpl) NewPlayerConnect(client ws.Client) {
	g.newPlayerStream <- common.NewPlayerEvent{Client: client}
}

// newPlayerConnect is when new socket joins, we send all of the current player to it
func (g *gameImpl) newPlayerConnect(client ws.Client) {
	go client.WritePump()
	go client.ReadPump()
	time.Sleep(100)
	clientID := client.GetID()
	// Send all current players info to new player
	initAllMsg := g.createInitAllMessage(g.objManager.GetPlayers(), g.objManager.GetMap())
	encodedMsg, _ := proto.Marshal(initAllMsg)
	// TODO: Hub might not finish registering client
	g.hub.Send(clientID, encodedMsg)

	// Send new player client ID
	registerClientIDMsg := &Message_proto.ServerGameMessage{
		Message: &Message_proto.ServerGameMessage_RegisterClientIdPayload{
			RegisterClientIdPayload: &Message_proto.RegisterClientID{
				ClientId: clientID,
			},
		},
	}
	encodedMsg, _ = proto.Marshal(registerClientIDMsg)
	g.hub.Send(clientID, encodedMsg)
}

// sendShootMsg send shoot event to all clients
func (g *gameImpl) sendShootMsg(shoot shootpkg.Shoot) {
	initShootMsg := &Message_proto.ServerGameMessage{
		Message: &Message_proto.ServerGameMessage_InitShootPayload{
			InitShootPayload: &Message_proto.Shoot{
				PlayerId: shoot.GetPlayerID(),
				Id:       shoot.GetID(),
				X:        shoot.GetX(),
				Y:        shoot.GetY(),
				Dx:       shoot.GetDX(),
				Dy:       shoot.GetDY(),
			},
		},
	}
	encodedMsg, _ := proto.Marshal(initShootMsg)
	g.hub.Broadcast(encodedMsg)
}

// Init current player with name sent from client
func (g *gameImpl) initPlayer(clientID int32, name string) {
	// Create new player
	// We register but client hasn't received the message, so it isn't enable
	player := g.objManager.RegisterPlayer(clientID, name)

	// Send new player info
	initPlayerMsg := &Message_proto.ServerGameMessage{
		Message: &Message_proto.ServerGameMessage_InitPlayerPayload{
			InitPlayerPayload: &Message_proto.InitPlayer{
				Id:     player.GetID(),
				Name:   player.GetName(),
				X:      player.GetPosition().X,
				Y:      player.GetPosition().Y,
				IsMain: true,
			},
		},
	}
	encodedMsg, _ := proto.Marshal(initPlayerMsg)
	g.hub.Send(clientID, encodedMsg)

	// Send all other players about new player info
	initPlayerMsg.GetInitPlayerPayload().IsMain = false
	encodedMsg, _ = proto.Marshal(initPlayerMsg)
	g.hub.BroadcastExclude(encodedMsg, clientID)
}

//  removePlayer remove player logic from game using player ID
//  This is game logic which
//    + Remove player from playerList
//    + Broadcast remove event to other
func (g *gameImpl) removePlayer(playerID int32, clientID int32) {
	rplayerID := g.objManager.RemovePlayer(playerID, clientID)

	// Send remove player event to all players
	removePlayerMsg := &Message_proto.ServerGameMessage{
		Message: &Message_proto.ServerGameMessage_RemovePlayerPayload{
			RemovePlayerPayload: &Message_proto.RemovePlayer{
				Id: rplayerID,
			},
		},
	}
	encodedMsg, _ := proto.Marshal(removePlayerMsg)
	g.hub.Broadcast(encodedMsg)
}

//  removePlayer remove player logic from game using player ID
//  This is game logic which
//    + Remove player from playerList
//    + Broadcast remove event to other
func (g *gameImpl) RemovePlayer(playerID int32, clientID int32) {
	g.removePlayer(playerID, clientID)
}

// RemovePlayerByClientID remove player from game using Client ID
// It only touch gamelogic, not the clients
func (g *gameImpl) RemovePlayerByClientID(clientID int32) {
	// TODO: Might block here, use eventStream for corresponding events
	fmt.Println("Game Remove player ClientID sent ", clientID, len(g.destroyPlayerStream), g.destroyPlayerStream)
	g.destroyPlayerStream <- common.DestroyPlayerEvent{
		ClientID: clientID,
		PlayerID: -1,
	}
	fmt.Println("Game Remove player ClientID done ", clientID)
}

// GetQuitChannel returns Quit channel for the outside
func (g *gameImpl) GetQuitChannel() chan bool {
	return g.quitChannel
}
