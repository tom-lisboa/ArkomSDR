package main

import (
	"arkom/bia/internal/agendaclient"
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"arkom/bia/internal/agent"
	"arkom/bia/internal/config"
	"arkom/bia/internal/contextworker"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"arkom/bia/internal/worker"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "Apply schema to the configured isolated database and exit")
	role := flag.String("role", "all", "Process role: api, context, execution, media, scheduling, followup, voice, all")
	flag.Parse()
	switch *role {
	case "api", "context", "execution", "media", "scheduling", "followup", "voice", "all":
	default:
		return errors.New("invalid role")
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, c.DSN)
	if err != nil {
		return errors.New("cannot connect to BIA_DATABASE_URL")
	}
	defer db.Close()
	if *migrate {
		return db.Migrate(ctx)
	}
	var planner agent.Planner = agent.Offline{}
	if c.OpenAIKey != "" {
		planner = agent.New(c.OpenAIKey, c.OpenAIModel, c.Policy.Brand, c.Policy.SchedulingURL)
	}
	var messenger providers.Messenger = providers.Disabled{}
	var twilioMessages *providers.TwilioMessaging
	if c.GatewayURL != "" {
		messenger = &providers.Gateway{URL: providers.TrimBase(c.GatewayURL), Token: c.GatewayToken, Client: providers.NewHTTP()}
	}
	if c.MessagingProvider == "twilio" {
		twilioMessages = &providers.TwilioMessaging{Pool: db.Pool, Account: c.Voice.Account, AuthToken: c.Voice.AuthToken, Sender: c.Voice.Sender, AllowedPhone: c.TestPhone, Client: providers.NewHTTP()}
		twilioMessages.FormContentSID = c.FormContentSID
		twilioMessages.PreparationContentSID = c.PreparationContentSID
		twilioMessages.IntroVideoContentSID = c.IntroVideoContentSID
		twilioMessages.BookedContentSID = c.BookedContentSID
		twilioMessages.BookedQuestionSID = c.BookedQuestionSID
		twilioMessages.TimeZone = c.Policy.TimeZone
		messenger = twilioMessages
	}
	calendar := &providers.LocalCalendar{Pool: db.Pool}
	w := &worker.Worker{VoiceAutoRequest: c.Voice.AutoRequest, VoiceSender: c.Voice.Sender, Queue: "execution", DB: db, Planner: planner, Messenger: messenger, Calendar: calendar, CRM: providers.LocalCRM{Pool: db.Pool}, Policy: c.Policy, Live: c.Mode == "live", PerLeadDaily: c.PerLeadDaily, GlobalHourly: c.GlobalHourly}
	if twilioMessages != nil {
		w.AudioSender = twilioMessages
		w.VideoSender = twilioMessages
	}
	if c.IntroVideoEnabled {
		w.IntroVideoURL = c.IntroVideo.URL()
	}
	if c.SMSFrom != "" {
		transport := &providers.TwilioMessaging{Pool: db.Pool, Account: c.Voice.Account, AuthToken: c.Voice.AuthToken, Client: providers.NewHTTP(), AllowedPhone: c.TestPhone}
		w.SMSSender = &providers.TwilioSMS{Transport: transport, From: c.SMSFrom}
	}
	sw := schedulingworker.New(*w, calendar)
	fw := followupworker.New(*w)
	fw.Audio = c.ReminderAudio
	fw.VoiceEnabled = c.Voice.Enabled
	var processor *providers.Media
	if c.OpenAIKey != "" && (len(c.MediaHosts) > 0 || twilioMessages != nil) {
		hosts := map[string]bool{}
		for _, h := range c.MediaHosts {
			hosts[h] = true
		}
		ai := openai.NewClient(option.WithAPIKey(c.OpenAIKey), option.WithMaxRetries(1))
		downloader := &providers.Downloader{Hosts: hosts, HTTP: providers.MediaHTTP()}
		if twilioMessages != nil {
			downloader.Account = c.Voice.Account
			downloader.AuthToken = c.Voice.AuthToken
		}
		processor = &providers.Media{Download: downloader, AI: ai, Model: c.MediaModel, Hosts: hosts, HTTP: providers.MediaHTTP()}
		if c.AudioModel != "" {
			processor.Audio = &providers.Audio{Download: downloader, AI: ai, Model: c.AudioModel, Hosts: hosts, HTTP: providers.MediaHTTP()}
		}
	}
	cw := &contextworker.Worker{DB: db, Policy: c.Window}
	if c.RedisURL != "" && (*role == "all" || *role == "context") {
		opts, e := redis.ParseURL(c.RedisURL)
		if e != nil {
			return errors.New("invalid BIA_REDIS_URL")
		}
		opts.MaxRetries = -1
		opts.DialTimeout = 200 * time.Millisecond
		opts.ReadTimeout = 200 * time.Millisecond
		opts.WriteTimeout = 200 * time.Millisecond
		client := redis.NewClient(opts)
		defer client.Close()
		cw.Buffer = &contextworker.Buffer{Client: client}
	}
	if twilioMessages != nil && c.Mode == "live" {
		cw.Typing = twilioMessages
		cw.TestPhone = c.TestPhone
	}
	mw := &mediaworker.Worker{DB: db, Outbound: mediaworker.Outbound(*w, c.ReminderAudio, c.IntroVideo)}
	if processor != nil {
		mw.Processor = processor
	}
	vr := voiceworker.Repository{DB: db}
	var vp voiceworker.Provider
	if c.Voice.Enabled {
		vp = &voiceworker.Twilio{AllowedPhone: c.TestPhone, Account: c.Voice.Account, AuthToken: c.Voice.AuthToken, Sender: c.Voice.Sender, ContentSID: c.Voice.ContentSID, PublicURL: c.Voice.PublicURL, Client: providers.NewHTTP(), MaxSeconds: int(c.Voice.MaxDuration.Seconds())}
	}
	agenda := agendaclient.Client{DB: db}
	vw := &voiceworker.Worker{Agenda: agenda, Repo: vr, Provider: vp, Policy: c.Policy, Live: c.Voice.Enabled, AllowOutsideSession: c.Voice.ApprovedOutsideSession}
	vs := &voiceworker.Server{Repo: vr, Provider: vp, Account: c.Voice.Account, Sender: c.Voice.Sender, AuthToken: c.Voice.AuthToken, PublicURL: c.Voice.PublicURL, Enabled: c.Voice.Enabled}
	vs.Messages, vs.Mode, vs.TimeZone, vs.TestPhone = twilioMessages, c.Mode, c.Policy.TimeZone, c.TestPhone
	if c.Voice.Enabled {
		vs.Bridge = &voiceworker.Bridge{Repo: vr, Provider: vp, Connect: voiceworker.OpenAIConnect(c.OpenAIKey, c.Voice.Model), Tools: voiceworker.Tools{Repo: vr, Agenda: agenda, Policy: c.Policy}, Model: c.Voice.Model, Voice: c.Voice.Voice, TranscriptionModel: c.Voice.TranscriptionModel, Brand: c.Policy.Brand, MaxDuration: c.Voice.MaxDuration}
	}
	var wg sync.WaitGroup
	start := func(f func(context.Context)) { wg.Add(1); go func() { defer wg.Done(); f(ctx) }() }
	if (*role == "all" || *role == "context") && cw.Typing != nil {
		start(cw.RunTyping)
	}
	if *role == "all" || *role == "followup" {
		start(fw.RunScheduler)
	}
	if *role == "all" || *role == "scheduling" {
		start(func(ctx context.Context) { sw.RunAvailability(ctx, c.Availability) })
	}
	for range c.Workers {
		if *role == "all" || *role == "voice" {
			start(vw.Run)
		}
		if *role == "all" || *role == "execution" {
			start(w.Run)
		}
		if *role == "all" || *role == "scheduling" {
			start(sw.Run)
		}
		if *role == "all" || *role == "followup" {
			start(fw.Run)
		}
		if *role == "all" || *role == "context" {
			start(cw.Run)
		}
		if *role == "all" || *role == "media" {
			start(mw.Run)
			start(mw.Outbound.Run)
		}
	}
	if *role != "all" && *role != "api" && *role != "voice" {
		slog.Info("worker running", "role", *role, "mode", c.Mode)
		<-ctx.Done()
		wg.Wait()
		return nil
	}
	api := &httpapi.Server{IntroVideo: c.IntroVideo, ReminderAudio: c.ReminderAudio, TestFormToken: c.TestFormToken, DB: db, Calendar: calendar, Token: c.Token, Mode: c.Mode, Policy: c.Policy, VoiceSender: c.Voice.Sender, VoiceEnabled: c.Voice.Enabled}
	api.FormAutoContact = c.FormAutoContact
	api.SMSEnabled = c.SMSFrom != ""
	srv := &http.Server{Addr: c.Address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	voiceSrv := &http.Server{Addr: c.Voice.Address, Handler: vs.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	serverErr := make(chan error, 2)
	if *role == "all" || *role == "voice" {
		go func() { serverErr <- voiceSrv.ListenAndServe() }()
	}
	if *role == "all" || *role == "api" {
		go func() { serverErr <- srv.ListenAndServe() }()
	}
	slog.Info("bia running", "address", c.Address, "mode", c.Mode, "llm_enabled", c.OpenAIKey != "")
	select {
	case <-ctx.Done():
	case e := <-serverErr:
		if !errors.Is(e, http.ErrServerClosed) {
			stop()
			wg.Wait()
			return e
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	_ = voiceSrv.Shutdown(shutdown)
	wg.Wait()
	return nil
}
