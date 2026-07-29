package server

import (
	"strings"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
)

func initServerAuthAndTags(config *configType) {
	globals.apiKeySalt = config.APIKeySalt

	if err := store.InitAuthLogicalNames(config.Auth["logical_names"]); err != nil {
		logs.Err.Fatal(err)
	}

	globals.immutableTagNS = make(map[string]bool)
	for _, name := range store.Store.GetAuthNames() {
		authHandler := store.Store.GetLogicalAuthHandler(name)
		if authHandler == nil {
			logs.Err.Fatalln("Unknown authenticator", name)
		}
		jsConfig := config.Auth[authHandler.GetRealName()]
		if jsConfig == nil {
			continue
		}
		if err := authHandler.Init(jsConfig, name); err != nil {
			logs.Err.Fatalln("Failed to init auth scheme", name+":", err)
		}
		tags, err := authHandler.RestrictedTags()
		if err != nil {
			logs.Err.Fatalln("Failed get restricted tag namespaces (prefixes)", name+":", err)
		}
		for _, tag := range tags {
			if strings.Contains(tag, ":") {
				logs.Err.Fatalln("tags restricted by auth handler should not contain character ':'", tag)
			}
			globals.immutableTagNS[tag] = true
		}
	}

	for name, validatorConfig := range config.Validator {
		if validatorConfig.AddToTags {
			if strings.Contains(name, ":") {
				logs.Err.Fatalln("acc_validation names should not contain character ':'", name)
			}
			globals.immutableTagNS[name] = true
		}
		if len(validatorConfig.Required) == 0 {
			continue
		}

		requiredLevels := make([]auth.Level, 0, len(validatorConfig.Required))
		for _, required := range validatorConfig.Required {
			level := auth.ParseAuthLevel(required)
			if level == auth.LevelNone {
				logs.Err.Fatalf("Invalid required AuthLevel '%s' in validator '%s'", required, name)
			}
			requiredLevels = append(requiredLevels, level)
			if globals.authValidators == nil {
				globals.authValidators = make(map[auth.Level][]string)
			}
			globals.authValidators[level] = append(globals.authValidators[level], name)
		}

		validator := store.Store.GetValidator(name)
		if validator == nil {
			logs.Err.Fatal("Config provided for an unknown validator '" + name + "'")
		}
		if err := validator.Init(string(validatorConfig.Config)); err != nil {
			logs.Err.Fatal("Failed to init validator '"+name+"': ", err)
		}
		if globals.validators == nil {
			globals.validators = make(map[string]credValidator)
		}
		globals.validators[name] = credValidator{
			requiredAuthLvl: requiredLevels,
			addToTags:       validatorConfig.AddToTags,
		}
	}

	if len(globals.authValidators) > 0 {
		globals.validatorClientConfig = make(map[string][]string)
		for level, validators := range globals.authValidators {
			globals.validatorClientConfig[level.String()] = validators
		}
	}

	globals.maskedTagNS = make(map[string]bool, len(config.MaskedTagNamespaces))
	for _, tag := range config.MaskedTagNamespaces {
		if strings.Contains(tag, ":") {
			logs.Err.Fatal("masked_tags namespaces should not contain character ':'", tag)
		}
		globals.maskedTagNS[tag] = true
	}

	config.AliasTagNamespace = strings.TrimSpace(config.AliasTagNamespace)
	if config.AliasTagNamespace != "" {
		if prefix, _ := validateTag(config.AliasTagNamespace + ":testing"); prefix == "" {
			logs.Err.Fatal("alias_tag namespace should contain only alphanumeric characters and '_'",
				config.AliasTagNamespace)
		}
		globals.aliasTagNS = config.AliasTagNamespace
	}

	var tags []string
	for tag := range globals.immutableTagNS {
		tags = append(tags, "'"+tag+"'")
	}
	if len(tags) > 0 {
		logs.Info.Println("Restricted tags:", tags)
	}
	tags = nil
	for tag := range globals.maskedTagNS {
		tags = append(tags, "'"+tag+"'")
	}
	if len(tags) > 0 {
		logs.Info.Println("Masked tags:", tags)
	}
	if globals.aliasTagNS != "" {
		logs.Info.Println("Alias tag:", globals.aliasTagNS)
	}
}
