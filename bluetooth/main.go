package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"tinygo.org/x/bluetooth"

	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
)

type flags struct {
	MAC   *cli.StringFlag
	Debug *cli.BoolFlag
	// ConnectionTimeout *cli.DurationFlag
}

func (f *flags) Set() []cli.Flag {
	return []cli.Flag{
		f.MAC,
		f.Debug,
		// f.ConnectionTimeout,
	}
}

type Bctl struct {
	flags     flags
	deviceMAC bluetooth.MAC

	adapter *adapter.Adapter1

	discoveryDone <-chan struct{}
}

func main() {
	var tool Bctl
	f := flags{
		MAC: &cli.StringFlag{
			Name:  "mac",
			Usage: "MAC adress of device connect to",
			Action: func(ctx *cli.Context, s string) error {
				mac, err := bluetooth.ParseMAC(s)
				if err != nil {
					return fmt.Errorf("incorrect mac address: %w", err)
				}
				tool.deviceMAC = mac
				return nil
			},
		},

		Debug: &cli.BoolFlag{
			Name:   "debug",
			Hidden: true,
		},
	}

	app := cli.App{
		Name:  "bctl",
		Usage: "Console utility to connect to bluetooth devices",
		Action: func(ctx *cli.Context) error {
			if f.Debug.Get(ctx) {
				logrus.SetLevel(logrus.DebugLevel)
			}

			if err := tool.Init(); err != nil {
				return err
			}

			cancel, err := tool.Discover(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer tool.Wait(ctx.Context)

			if err := tool.Connect(ctx); err != nil {
				return err
			}

			cancel()

			return nil
		},
		Flags: f.Set(),
	}

	app.RunAndExitOnError()
}

func (cli *Bctl) Init() error {
	var err error
	cli.adapter, err = adapter.GetDefaultAdapter()
	if err != nil {
		return fmt.Errorf("get default adapter: %w", err)
	}
	return nil
}

func (cli *Bctl) Discover(ctx *cli.Context) (_ context.CancelFunc, err error) {
	filter := adapter.NewDiscoveryFilter()
	// filter.Transport = adapter.DiscoveryFilterTransportBrEdr
	devices, cancel, err := api.Discover(cli.adapter, &filter)
	if err != nil {
		return nil, fmt.Errorf("discover devices: %w", err)
	}

	done := make(chan struct{})

	cli.discoveryDone = done

	cancelOnce := func() func() {
		var once sync.Once
		return func() {
			once.Do(cancel)
		}
	}()

	go func() {
		defer close(done)
		defer cancelOnce()

		logrus.Info("discovery started")

		for gotDev := range devices {
			logrus.Trace("scanned device", gotDev.Path)
			d, err := device.NewDevice1(gotDev.Path)
			if err != nil {
				logrus.WithError(err).Trace("create device by dbus path")
				continue
			}
			deviceAddr, err := d.GetAddress()
			if err != nil {
				logrus.WithError(err).Trace("get device address failed")
				continue
			}

			if deviceAddr == cli.deviceMAC.String() {
				logrus.Infof("expected device found")
				return
			}
		}
	}()

	return cancelOnce, err
}

func (cli *Bctl) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-cli.discoveryDone:
		return nil
	}
}

var (
	errAlreadyPaired    = errors.New("Already Paired")
	errAlreadyConnected = errors.New("Already Paired")
)

func (cli *Bctl) Connect(ctx *cli.Context) error {
	const retryInterval = 3 * time.Second
	tick := time.NewTicker(retryInterval)
	defer tick.Stop()
	once := make(chan struct{}, 1)
	once <- struct{}{}

	adapterID, err := cli.adapter.GetAdapterID()
	if err != nil {
		return fmt.Errorf("get adapter id: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		case <-once:
		}

		if err := cli.connect(adapterID); err != nil {
			logrus.WithError(err).WithField("retry", retryInterval).Info("try to connect")
		} else {
			return nil
		}
	}
}

func (cli *Bctl) connect(adapterID string) error {
	d, err := device.NewDevice(adapterID, cli.deviceMAC.String())
	if err != nil {
		return fmt.Errorf("get device mac: %w", err)
	}

	paired, err := d.GetPaired()
	if err != nil {
		return fmt.Errorf("check already paired: %w", err)
	}
	if !paired {
		if err := d.Pair(); err != nil {
			return fmt.Errorf("pair with device: %w", err)
		}
	}

	if err := d.Connect(); err != nil {
		return fmt.Errorf("connect to device: %w", err)
	}

	logrus.Info("device connected successfully")
	return nil
}
