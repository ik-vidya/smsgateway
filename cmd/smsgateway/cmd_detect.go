// Команда smsgateway, файл cmd_detect.go — подкоманда detect-modem.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ik-vidya/smsgateway/internal/modem"
)

func cmdDetectModem() *cobra.Command {
	var (
		asJSON    bool
		skipAT    bool
		atTimeout time.Duration
	)

	c := &cobra.Command{
		Use:   "detect-modem",
		Short: "Найти подключённые модемы и показать их параметры",
		Long: `Сканирует последовательные порты, читает метаданные USB из /sys,
стабильные пути из /dev/serial/by-id и /dev/serial/by-path,
опрашивает модем командами AT, AT+CGMI, AT+CGMM, AT+CGMR, AT+CGSN.

Колонка STABLE-ID — ключевая для запуска нескольких инстансов
smsgateway на одной машине: /dev/ttyUSB* переезжает между
перезагрузками, а by-id остаётся привязан к конкретному модему.

С --skip-at порт не открывается: полезно, если модем уже занят
запущенным сервисом. В этом случае AT-поля пусты.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDetectModem(asJSON, skipAT, atTimeout)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false,
		"вывести JSON вместо таблицы")
	c.Flags().BoolVar(&skipAT, "skip-at", false,
		"не опрашивать модем (только порты и метаданные)")
	c.Flags().DurationVar(&atTimeout, "at-timeout", 2*time.Second,
		"таймаут одной AT-команды")
	return c
}

func runDetectModem(asJSON, skipAT bool, atTimeout time.Duration) error {
	ctx := context.Background()

	infos, err := modem.DiscoverPorts(ctx, modem.DiscoverConfig{
		SkipAT:    skipAT,
		ATTimeout: atTimeout,
	})
	if err != nil {
		return err
	}

	if len(infos) == 0 {
		if asJSON {
			fmt.Println("[]")
		} else {
			fmt.Println("Последовательные порты не найдены.")
		}
		return nil
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(infos)
	}

	printPortTable(infos, skipAT)
	return nil
}

func printPortTable(infos []modem.PortInfo, skipAT bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if skipAT {
		fmt.Fprintln(w, "PORT\tSTABLE-ID\tVID:PID\tSERIAL\tPRODUCT")
	} else {
		fmt.Fprintln(w, "PORT\tSTABLE-ID\tVID:PID\tIMEI\tMODEL")
	}
	for _, p := range infos {
		vidpid := ""
		if p.VID != "" || p.PID != "" {
			vidpid = strings.ToUpper(p.VID + ":" + p.PID)
		}
		stable := p.StablePath
		if stable == "" {
			stable = p.ByPathPath
		}
		if stable == "" {
			stable = "-"
		}

		if skipAT {
			serial := p.SerialNumber
			if serial == "" {
				serial = "-"
			}
			product := p.Product
			if product == "" {
				product = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				p.Port, stable, orDash(vidpid), serial, product)
		} else {
			model := p.ModemModel
			if model == "" {
				model = p.ATError
			}
			if model == "" {
				model = "-"
			}
			imei := p.IMEI
			if imei == "" {
				imei = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				p.Port, stable, orDash(vidpid), imei, model)
		}
	}
	_ = w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
