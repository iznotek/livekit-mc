// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"bytes"

	"github.com/pion/webrtc/v3"
	"github.com/urfave/cli/v2"

	provider2 "github.com/livekit/livekit-cli/pkg/provider"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
)

var (
	JoinCommands = []*cli.Command{
		{
			Name:     "join-room",
			Usage:    "Joins a room as a participant",
			Action:   joinRoom,
			Category: "Simulate",
			Flags: withDefaultFlags(
				roomFlag,
				identityFlag,
				&cli.BoolFlag{
					Name:  "publish-demo",
					Usage: "publish demo video as a loop",
				},
				&cli.StringSliceFlag{
					Name: "publish",
					Usage: "files to publish as tracks to room (supports .h264, .ivf, .ogg). " +
						"can be used multiple times to publish multiple files. " +
						"can publish from Unix or TCP socket using the format `codec://socket_name` or `codec://host:address` respectively. Valid codecs are h264, vp8, opus",
				},
				&cli.Float64Flag{
					Name:  "fps",
					Usage: "if video files are published, indicates FPS of video",
				},
			),
		},
	}
)

const mimeDelimiter = "://"
const MimeTypeDataByte = "data/byte"

func joinRoom(c *cli.Context) error {
	pc, err := loadProjectDetails(c)
	if err != nil {
		return err
	}

	roomCB := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnDataReceived: func(data []byte, rp *lksdk.RemoteParticipant) {
				identity := ""
				if rp != nil {
					identity = rp.Identity()
				}
				logger.Infow("received data", "data", data, "participant", identity)
			},
			OnConnectionQualityChanged: func(update *livekit.ConnectionQualityInfo, p lksdk.Participant) {
				logger.Debugw("connection quality changed", "participant", p.Identity(), "quality", update.Quality)
			},
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
				logger.Infow("track subscribed",
					"kind", pub.Kind(),
					"trackID", pub.SID(),
					"source", pub.Source(),
					"participant", participant.Identity(),
				)
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
				logger.Infow("track unsubscribed",
					"kind", pub.Kind(),
					"trackID", pub.SID(),
					"source", pub.Source(),
					"participant", participant.Identity(),
				)
			},
			OnTrackUnpublished: func(pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
				logger.Infow("track unpublished",
					"kind", pub.Kind(),
					"trackID", pub.SID(),
					"source", pub.Source(),
					"participant", participant.Identity(),
				)
			},
			OnTrackMuted: func(pub lksdk.TrackPublication, participant lksdk.Participant) {
				logger.Infow("track muted",
					"kind", pub.Kind(),
					"trackID", pub.SID(),
					"source", pub.Source(),
					"participant", participant.Identity(),
				)
			},
			OnTrackUnmuted: func(pub lksdk.TrackPublication, participant lksdk.Participant) {
				logger.Infow("track unmuted",
					"kind", pub.Kind(),
					"trackID", pub.SID(),
					"source", pub.Source(),
					"participant", participant.Identity(),
				)
			},
		},
		OnRoomMetadataChanged: func(metadata string) {
			logger.Infow("room metadata changed", "metadata", metadata)
		},
		OnReconnecting: func() {
			logger.Infow("reconnecting to room")
		},
		OnReconnected: func() {
			logger.Infow("reconnected to room")
		},
		OnDisconnected: func() {
			logger.Infow("disconnected from room")
		},
	}

	var room *lksdk.Room

	if pc.Token != "" {
		_room, err := lksdk.ConnectToRoomWithToken(pc.URL, pc.Token, roomCB)
		if err != nil {
			return err
		}
		room = _room
		defer room.Disconnect()
	} else {
		_room, err := lksdk.ConnectToRoom(pc.URL, lksdk.ConnectInfo{
			APIKey:              pc.APIKey,
			APISecret:           pc.APISecret,
			RoomName:            c.String("room"),
			ParticipantIdentity: c.String("identity"),
		}, roomCB)
		if err != nil {
			return err
		}
		room = _room
		defer room.Disconnect()
	}



	logger.Infow("connected to room", "room", room.Name())

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	if c.Bool("publish-demo") {
		if err = publishDemo(room); err != nil {
			return err
		}
	}

	if c.StringSlice("publish") != nil {
		fps := c.Float64("fps")
		for _, pub := range c.StringSlice("publish") {
			if err = handlePublish(room, pub, fps); err != nil {
				return err
			}
		}
	}

	<-done
	return nil
}

func handlePublish(room *lksdk.Room, name string, fps float64) error {
	// See if we're dealing with a socket
	if isSocketFormat(name) {
		mimeType, socketType, address, err := parseSocketFromName(name)
		if err != nil {
			return err
		}
		return publishSocket(room, mimeType, socketType, address, fps)
	}
	// Else, handle file
	return publishFile(room, name, fps)
}

func publishDemo(room *lksdk.Room) error {
	var tracks []*lksdk.LocalSampleTrack

	loopers, err := provider2.CreateVideoLoopers("high", "", true)
	if err != nil {
		return err
	}
	for i, looper := range loopers {
		layer := looper.ToLayer(livekit.VideoQuality(i))
		track, err := lksdk.NewLocalSampleTrack(looper.Codec(),
			lksdk.WithSimulcast("demo-video", layer),
		)
		if err != nil {
			return err
		}
		if err = track.StartWrite(looper, nil); err != nil {
			return err
		}
		tracks = append(tracks, track)
	}
	_, err = room.LocalParticipant.PublishSimulcastTrack(tracks, &lksdk.TrackPublicationOptions{
		Name: "demo",
	})
	return err
}

func publishFile(room *lksdk.Room, filename string, fps float64) error {
	// Configure provider
	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			fmt.Println("finished writing file", filename)
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	// Set frame rate if it's a video stream and FPS is set
	ext := filepath.Ext(filename)
	if ext == ".h264" || ext == ".ivf" {
		if fps != 0 {
			frameDuration := time.Second / time.Duration(fps)
			opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
		}
	}

	// Create track and publish
	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	if err != nil {
		return err
	}
	pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: filename,
	})
	return err
}

func parseSocketFromName(name string) (string, string, string, error) {
	// Extract mime type, socket type, and address
	// e.g. h264://192.168.0.1:1234 (tcp)
	// e.g. opus:///tmp/my.socket (unix domain socket)

	offset := strings.Index(name, mimeDelimiter)
	if offset == -1 {
		return "", "", "", fmt.Errorf("did not find delimiter %s in %s", mimeDelimiter, name)
	}

	mimeType := name[:offset]

	if mimeType != "data" && mimeType != "h264" && mimeType != "vp8" && mimeType != "opus" {
		return "", "", "", fmt.Errorf("unsupported mime type: %s", mimeType)
	}

	address := name[offset+len(mimeDelimiter):]

	if len(address) == 0 {
		return "", "", "", fmt.Errorf("address cannot be empty. input was: %s", name)
	}

	// If the address doesn't contain a ':' we assume it's a unix socket
	if !strings.Contains(address, ":") {
		return mimeType, "unix", address, nil
	}

	return mimeType, "tcp", address, nil
}

func isSocketFormat(name string) bool {
	return strings.Contains(name, mimeDelimiter)
}

func publishSocket(room *lksdk.Room, mimeType string, socketType string, address string, fps float64) error {
	var mime string
	switch {
	case strings.Contains(mimeType, "data"):
		mime = MimeTypeDataByte
	case strings.Contains(mimeType, "h264"):
		mime = webrtc.MimeTypeH264
	case strings.Contains(mimeType, "vp8"):
		mime = webrtc.MimeTypeVP8
	case strings.Contains(mimeType, "opus"):
		mime = webrtc.MimeTypeOpus
	default:
		return lksdk.ErrUnsupportedFileType
	}

	// Dial socket
	sock, err := net.Dial(socketType, address)
	if err != nil {
		return err
	}

	// Publish to room
	err = publishReader(room, sock, mime, fps)
	return err
}

func publishReader(room *lksdk.Room, in io.ReadCloser, mime string, fps float64) error {
	// Configure provider
	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			fmt.Printf("finished writing %s stream\n", mime)
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	// Set frame rate if it's a video stream and FPS is set
	if strings.EqualFold(mime, webrtc.MimeTypeVP8) ||
		strings.EqualFold(mime, webrtc.MimeTypeH264) {
		if fps != 0 {
			frameDuration := time.Second / time.Duration(fps)
			opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
		}
	}

	if ( mime != MimeTypeDataByte) {
		// Create track and publish
		track, err := lksdk.NewLocalReaderTrack(in, mime, opts...)
		if err != nil {
			return err
		}
		pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{})
		if err != nil {
			return err
		}
	} else {
		// Loop over read and publish 
		for {
			data := new(bytes.Buffer)
			numOfByte, err := data.ReadFrom(in)
			if err != nil {
				return err
			}

			if (numOfByte > 0) {
				if( data.String() == "EXIT") {
					return fmt.Errorf("finished writing %s stream", mime)
				}
				fmt.Printf(data.String() + "\n")

				err = room.LocalParticipant.PublishData(data.Bytes(), livekit.DataPacket_RELIABLE, nil)
				if err != nil {
					return err
				}
			} 
				
			time.Sleep(10*time.Millisecond)
		}
	}
	return nil
}
