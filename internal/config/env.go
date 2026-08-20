package config

import (
	"crypto/sha256"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Demo passwords. They are deliberately obvious: demo mode announces them on
// the login page, so they are documentation rather than secrets.
const (
	DemoPassword      = "demo"
	DemoAdminPassword = "admin"
)

// LoadRuntime reads deployment settings from the environment. The shared
// password is required; everything else has a usable default.
//
// Demo mode is the exception: it fills in throwaway passwords so someone who
// just cloned the repository can start the site with no configuration at all.
func LoadRuntime() (Runtime, error) {
	demo := envBool("DEMO", false)
	rt := Runtime{
		Demo:          demo,
		ListenAddr:    env("LISTEN_ADDR", ":8080"),
		ConfigPath:    env("CONFIG_PATH", "config.yaml"),
		DBPath:        env("DB_PATH", "data/dinners.db"),
		BaseURL:       strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		Password:      os.Getenv("DINNER_PASSWORD"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		SessionMaxAge: time.Duration(envInt("SESSION_DAYS", 90)) * 24 * time.Hour,
		TrustProxy:    envBool("TRUST_PROXY", true),
		Mail: MailSettings{
			Host:       os.Getenv("SMTP_HOST"),
			Port:       envInt("SMTP_PORT", 587),
			Username:   os.Getenv("SMTP_USER"),
			Password:   os.Getenv("SMTP_PASSWORD"),
			From:       os.Getenv("SMTP_FROM"),
			FromName:   env("SMTP_FROM_NAME", "Rudbeckia middagar"),
			Encryption: strings.ToLower(env("SMTP_ENCRYPTION", "starttls")),
			ReplyTo:    os.Getenv("SMTP_REPLY_TO"),
			BCC:        os.Getenv("SMTP_BCC"),
		},
	}
	if demo {
		if rt.Password == "" {
			rt.Password = DemoPassword
		}
		if rt.AdminPassword == "" {
			rt.AdminPassword = DemoAdminPassword
		}
	}
	if rt.Password == "" {
		return rt, errors.New("DINNER_PASSWORD must be set (or run with -demo to try the site out)")
	}
	// A stable secret keeps sessions alive across restarts. Deriving it from
	// the passwords means rotating a password also invalidates every session
	// and every guest link, which is exactly what you want when someone moves
	// out.
	if s := os.Getenv("SESSION_SECRET"); s != "" {
		sum := sha256.Sum256([]byte(s))
		rt.SessionSecret = sum[:]
	} else {
		sum := sha256.Sum256([]byte("rudbeckia-middag|" + rt.Password + "|" + rt.AdminPassword))
		rt.SessionSecret = sum[:]
	}
	switch rt.Mail.Encryption {
	case "starttls", "tls", "none":
	default:
		return rt, errors.New(`SMTP_ENCRYPTION must be one of "starttls", "tls", "none"`)
	}
	return rt, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
