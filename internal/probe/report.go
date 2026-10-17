// Package probe, файл report.go — текстовый отчёт и рекомендации.
package probe

import (
	"fmt"
	"io"
	"strings"
)

// RenderHuman пишет отчёт в человекочитаемом виде.
func (r *Result) RenderHuman(w io.Writer) {
	fmt.Fprintf(w, "=== probe: %s ===\n\n", r.Port)

	fmt.Fprintln(w, "Идентификация:")
	fmt.Fprintf(w, "  ATI:    %s\n", orDash(r.ATI))
	fmt.Fprintf(w, "  CGMI:   %s\n", orDash(r.CGMI))
	fmt.Fprintf(w, "  CGMM:   %s\n", orDash(r.CGMM))
	fmt.Fprintf(w, "  CGMR:   %s\n", orDash(r.CGMR))
	fmt.Fprintf(w, "  CGSN:   %s\n", orDash(r.CGSN))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "SIM:")
	fmt.Fprintf(w, "  CPIN:   %s\n", orDash(r.CPIN))
	fmt.Fprintf(w, "  CCID:   %s\n", orDash(r.CCID))
	fmt.Fprintf(w, "  CIMI:   %s\n", orDash(r.CIMI))
	fmt.Fprintf(w, "  SPIC:   %s  (%s)\n",
		orDash(r.SPIC), hasLabel(r.HasSPIC))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Сеть:")
	fmt.Fprintf(w, "  CSQ:    %s\n", orDash(r.CSQ))
	fmt.Fprintf(w, "  CREG:   %s\n", orDash(r.CREG))
	fmt.Fprintf(w, "  CGREG:  %s\n", orDash(r.CGREG))
	fmt.Fprintf(w, "  COPS:   %s\n", orDash(r.COPS))
	if r.COPSRaw != "" {
		fmt.Fprintf(w, "  COPS=?: %s\n", r.COPSRaw)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "SMS:")
	fmt.Fprintf(w, "  CPMS:   %s\n", orDash(r.CPMS))
	fmt.Fprintf(w, "  CMGF:   %s\n", orDash(r.CMGF))
	fmt.Fprintf(w, "  CSMP?:  %s  (%s)\n",
		orDash(r.CSMP), hasLabel(r.HasCSMP))
	fmt.Fprintf(w, "  CNMI?:  %s  (%s)\n",
		orDash(r.CNMI), hasLabel(r.HasCNMI))
	fmt.Fprintf(w, "  CSCS?:  %s  (%s)\n",
		orDash(r.CSCS), hasLabel(r.HasCSCS))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Питание и режим:")
	fmt.Fprintf(w, "  CBC:    %s  (%s)\n",
		orDash(r.CBC), hasLabel(r.HasCBC))
	fmt.Fprintf(w, "  CFUN?:  %s  (%s)\n",
		orDash(r.CFUN), hasLabel(r.HasCFUN))
	fmt.Fprintf(w, "  CBAND?: %s  (%s)\n",
		orDash(r.CBAND), hasLabel(r.HasCBAND))
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Определённый профиль: %s\n", r.Profile.Name())
	if r.Info.Manufacturer != "" || r.Info.Model != "" {
		fmt.Fprintf(w, "  (Auto() по Manufacturer=%q, Model=%q)\n",
			r.Info.Manufacturer, r.Info.Model)
	} else {
		fmt.Fprintln(w, "  (Auto() не смог идентифицировать модем)")
	}
	fmt.Fprintln(w)

	if len(r.Recommendations) > 0 {
		fmt.Fprintln(w, "Рекомендации:")
		for _, rec := range r.Recommendations {
			fmt.Fprintf(w, "  - %s\n", rec)
		}
		fmt.Fprintln(w)
	}

	if len(r.Errors) > 0 {
		fmt.Fprintln(w, "Ошибки отдельных команд (не фатальны):")
		for cmd, err := range r.Errors {
			fmt.Fprintf(w, "  %s: %s\n", cmd, err)
		}
		fmt.Fprintln(w)
	}
}

func orDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	// Схлопываем многострочные ответы в одну строку.
	s = strings.ReplaceAll(s, "\n", " | ")
	return s
}

func hasLabel(has bool) string {
	if has {
		return "есть"
	}
	return "нет"
}

// buildRecommendations собирает рекомендации по обходам.
//
// Логика:
//   - если профиль != Generic (например, sim800), перечисляем
//     его активные обходы — они уже включены в коде, но
//     полезно показать пользователю;
//   - если профиль Generic, но есть признаки SIM800 (SIMCOM/SIM800
//     в Manufacturer/Model), рекомендуем явно установить
//     MODEM_PROFILE=sim800 — Auto() его уже выбрал, но это
//     подсказка на случай, если пользователь форсирует
//     MODEM_PROFILE=generic;
//   - если HasSPIC — рекомендуем PinAttemptsAT=AT+SPIC;
//   - прочие общие подсказки.
func buildRecommendations(r *Result) []string {
	var out []string

	w := r.Profile.Workarounds()
	if w.ForceUCS2ForAllSMS {
		out = append(out,
			"ForceUCS2ForAllSMS: включён в профиле (обход бага GSM-7)")
	}
	if w.UseCSMPForSRR {
		out = append(out,
			"UseCSMPForSRR: включён в профиле (иначе DR не приходят)")
	}
	if w.AlignReadStorage {
		out = append(out,
			"AlignReadStorage: включён в профиле (read ≠ receive)")
	}
	if w.PinAttemptsAT != "" {
		out = append(out, fmt.Sprintf(
			"PinAttemptsAT: %s (проверка попыток PIN)", w.PinAttemptsAT))
	}

	if r.Profile.Name() == "generic" && looksLikeSIM800(r) {
		out = append(out,
			"модем похож на SIM800, но профиль generic — "+
				"установите MODEM_PROFILE=sim800")
	}

	if r.HasSPIC && w.PinAttemptsAT == "" {
		out = append(out,
			"модем поддерживает AT+SPIC — можно добавить "+
				"PinAttemptsAT в профиль")
	}

	if r.HasCBAND && !strings.Contains(r.CBAND, "ALL") {
		out = append(out, fmt.Sprintf(
			"AT+CBAND=%s — проверьте, что диапазон соответствует "+
				"вашему оператору", extractCBand(r.CBAND)))
	}

	if strings.Contains(strings.ToUpper(r.CPMS), "\"SM\"") &&
		strings.Contains(strings.ToUpper(r.CPMS), "\"ME\"") {
		out = append(out,
			"CPMS показывает SM и ME — при чтении SMS полезен "+
				"AlignReadStorage (как на SIM800)")
	}

	return out
}

// looksLikeSIM800 — эвристика по Manufacturer/Model.
func looksLikeSIM800(r *Result) bool {
	m := strings.ToUpper(r.Info.Manufacturer)
	mod := strings.ToUpper(r.Info.Model)
	return strings.Contains(m, "SIMCOM") || strings.Contains(mod, "SIM800") ||
		strings.Contains(mod, "SIM900")
}

// extractCBand достаёт значение из "+CBAND: \"EGSM900\"".
func extractCBand(s string) string {
	i := strings.Index(s, ":")
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(s[i+1:])
	v = strings.Trim(v, `"`)
	return v
}
