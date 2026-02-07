package web

import "embed"

//go:embed index.html favicon.png logo.png css/* js/* js/lib/*
var Assets embed.FS
