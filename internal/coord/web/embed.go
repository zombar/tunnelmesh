package web

import "embed"

//go:embed index.html favicon.png logo.png css/* js/*
var Assets embed.FS
