// Package twcfg holds a custom-configured Tailwind class merger behind gsx's
// canonical func([]string) string seam.
package twcfg

import twmerge "github.com/jackielii/tailwind-merge-go/pkg/twmerge"

var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig())

// Merge is what gsx.toml names. Exactly func([]string) string → direct reference.
func Merge(classes []string) string { return merger(classes) }
