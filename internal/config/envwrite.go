// Package config, файл envwrite.go — точечная правка .env.
//
// Задача: изменить значение одной или нескольких переменных в
// существующем .env, не потеряв комментарии, порядок строк и
// остальные переменные. Используется командой
// smsgateway detect-modem --write-config.
//
// Правила:
//
//   - Строки, не содержащие "=", переносятся как есть
//     (комментарии, пустые, заголовки).
//   - Строка "KEY=old" заменяется на "KEY=new".
//   - Строка "#KEY=old" или "# KEY=old" — раскомментируется и
//     заменяется на "KEY=new" (пользователь явно хотел бы её
//     включить).
//   - Если ключа нет — добавляется в конец с заголовком
//     "# Добавлено smsgateway".
//   - Если значение совпадает — строка не меняется (идемпотентно).
//   - Inline-комментарии вроде "KEY=value # comment" теряются:
//     парсер godotenv их не поддерживает, .env.example их не
//     использует.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UpdateEnvFile обновляет переменные в файле .env.
//
// Если файла нет — создаёт с указанными переменными. Родительский
// каталог создаётся при необходимости.
//
// Возвращает список применённых изменений в виде "KEY=value" —
// для логов.
func UpdateEnvFile(path string, updates map[string]string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("путь к .env не задан")
	}
	if len(updates) == 0 {
		return nil, nil
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("создание каталога %q: %w", dir, err)
		}
	}

	var (
		lines   []string
		applied []string
	)

	if _, err := os.Stat(path); err == nil {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("открытие %q: %w", path, err)
		}
		sc := bufio.NewScanner(file)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		scanErr := sc.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("чтение %q: %w", path, scanErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("закрытие %q: %w", path, closeErr)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}

	seen := make(map[string]bool, len(updates))
	for i, raw := range lines {
		key, _, commented := splitEnvLine(raw)
		if key == "" {
			continue
		}
		newVal, ok := updates[key]
		if !ok {
			continue
		}
		if commented {
			// Раскомментируем.
			lines[i] = key + "=" + newVal
			applied = append(applied, key+"="+newVal)
			seen[key] = true
			continue
		}
		// Активная строка: обновляем, если значение отличается.
		if envValueEquals(raw, newVal) {
			seen[key] = true
			continue
		}
		lines[i] = key + "=" + newVal
		applied = append(applied, key+"="+newVal)
		seen[key] = true
	}

	// Ключи, которых не было в файле.
	var missing []string
	for k := range updates {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "# Добавлено smsgateway detect-modem")
		for _, k := range missing {
			lines = append(lines, k+"="+updates[k])
			applied = append(applied, k+"="+updates[k])
		}
	}

	// Идемпотентность: если ничего не менялось — файл не трогаем.
	if len(applied) == 0 {
		return nil, nil
	}

	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("запись %q: %w", path, err)
	}
	return applied, nil
}

// splitEnvLine разбирает строку .env.
//
// Возвращает (key, value, commented). Пустой key — строка не
// содержит переменной (комментарий-заголовок, пустая строка).
//
// Поддерживаются формы:
//
//	KEY=value
//	KEY="value"
//	#KEY=value
//	# KEY=value
//
// Значение обрезается по пробелам, кавычки снимаются.
func splitEnvLine(line string) (key, value string, commented bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "#") {
		commented = true
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	}
	if s == "" {
		return "", "", commented
	}
	eq := strings.Index(s, "=")
	if eq <= 0 {
		return "", "", commented
	}
	key = strings.TrimSpace(s[:eq])
	value = strings.TrimSpace(s[eq+1:])
	value = strings.Trim(value, `"`)
	return key, value, commented
}

// envValueEquals проверяет, что активная строка KEY=... имеет то
// же значение, что newVal, с точностью до кавычек и пробелов.
func envValueEquals(line, newVal string) bool {
	_, v, commented := splitEnvLine(line)
	if commented {
		return false
	}
	return v == newVal
}
