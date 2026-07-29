package server

import (
	"errors"
	"slices"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// updateUserAuth 认证更新
func updateUserAuth(msg *ClientComMessage, user *types.User, _ *auth.Rec, remoteAddr string) error {
	authhdl := store.Store.GetLogicalAuthHandler(msg.Acc.Scheme)
	if authhdl != nil {
		rec, err := authhdl.UpdateRecord(&auth.Rec{Uid: user.Uid(), Tags: user.Tags}, msg.Acc.Secret, remoteAddr)
		if errors.Is(err, types.ErrNotFound) || err == types.ErrNotFound {
			// 若该方案在此用户上尚无记录，作为新认证方案添加
			rec, err = authhdl.AddRecord(&auth.Rec{Uid: user.Uid(), Tags: user.Tags}, msg.Acc.Secret, remoteAddr)
		}
		if err != nil {
			return err
		}

		// 标签可能已被 authhdl 更改，重置它们
		// 此处无法处理错误，仅记录日志但不返回
		if _, err = store.Users.UpdateTags(user.Uid(), nil, nil, rec.Tags); err != nil {
			logs.Warn.Println("updateUserAuth tags update failed:", err)
		}
		return nil
	}

	// 无效或未知认证方案
	return types.ErrMalformed
}

// addCreds 添加新凭证并重新发送现有凭证的验证请求
// 必要时还会添加凭证定义的标签
// 返回仅在此调用中验证的方法，返回完整的标签集
// 或当标签未变时返回 nil
func addCreds(uid types.Uid, creds []MsgCredClient, extraTags []string,
	lang string, tmpToken []byte) ([]string, []string, error) {
	var validated []string
	for i := range creds {
		cr := &creds[i]
		vld := store.Store.GetValidator(cr.Method)
		if vld == nil || !vld.IsInitialized() {
			// 忽略未知或未初始化的验证器
			continue
		}

		isNew, err := vld.Request(uid, cr.Value, lang, cr.Response, tmpToken)
		if err != nil {
			return nil, nil, err
		}

		if isNew && cr.Response != "" {
			// 如果提供了响应且 vld.Request 未返回错误，说明新请求验证成功
			validated = append(validated, cr.Method)

			// 为已确认的凭证生成标签
			if globals.validators[cr.Method].addToTags {
				extraTags = append(extraTags, cr.Method+":"+cr.Value)
			}
		}
	}

	// 保存验证器可能已更改的标签
	if len(extraTags) > 0 {
		if utags, err := store.Users.UpdateTags(uid, extraTags, nil, nil); err == nil {
			extraTags = utags
		} else {
			logs.Warn.Println("add cred tags update failed:", err)
		}
	} else {
		extraTags = nil
	}
	return validated, extraTags, nil
}

// validatedCreds 返回已验证的凭证列表，包括本次调用中验证的凭证。
// 返回所有已验证的方法（包括之前和本次验证的）。
// 返回完整的标签集，或标签未变时返回 nil。
func validatedCreds(uid types.Uid, authLvl auth.Level, creds []MsgCredClient,
	errorOnFail bool) ([]string, []string, error) {
	// 检查是否需要凭证验证
	if len(globals.authValidators[authLvl]) == 0 {
		return nil, nil, nil
	}

	// 获取所有已验证的方法
	allCreds, err := store.Users.GetAllCreds(uid, "", true)
	if err != nil {
		return nil, nil, err
	}

	methods := make(map[string]struct{})
	for i := range allCreds {
		methods[allCreds[i].Method] = struct{}{}
	}

	// 添加本次调用中验证的凭证。
	// 移除未知的验证器
	creds = normalizeCredentials(creds, false)
	var tagsToAdd []string
	for i := range creds {
		cr := &creds[i]
		if cr.Response == "" {
			// 忽略空响应
			continue
		}

		// 无需检查 nil，未知方法已在前面移除
		vld := store.Store.GetValidator(cr.Method)
		value, err := vld.Check(uid, cr.Response)

		if err != nil {
			// 检查失败
			if storeErr, ok := err.(types.StoreError); ok && storeErr == types.ErrCredentials {
				if errorOnFail {
					// 报告无效响应
					return nil, nil, types.ErrInvalidResponse
				}
				// 跳过无效响应，凭证保持未验证状态
				continue
			}
			// 实际错误，向上报告
			return nil, nil, err
		}

		// 检查未返回错误：请求验证成功
		methods[cr.Method] = struct{}{}

		// 将已验证的凭证添加到用户标签
		if globals.validators[cr.Method].addToTags {
			tagsToAdd = append(tagsToAdd, cr.Method+":"+value)
		}
	}

	var tags []string
	if len(tagsToAdd) > 0 {
		// 保存标签更新
		if utags, err := store.Users.UpdateTags(uid, tagsToAdd, nil, nil); err == nil {
			tags = utags
		} else {
			logs.Warn.Println("validated creds tags update failed:", err)
			tags = nil
		}
	} else {
		tags = nil
	}

	validated := make([]string, 0, len(methods))
	for method := range methods {
		validated = append(validated, method)
	}

	return validated, tags, nil
}

// deleteCred 删除用户凭证。
// 返回完整的剩余标签集，或标签未变时返回 nil。
func deleteCred(uid types.Uid, authLvl auth.Level, cred *MsgCredClient) ([]string, error) {
	vld := store.Store.GetValidator(cred.Method)
	if vld == nil || cred.Value == "" {
		// 拒绝无效请求：未知验证方法或缺少凭证值
		return nil, types.ErrMalformed
	}

	// 此验证级别是否要求该凭证？
	isRequired := slices.Contains(globals.authValidators[authLvl], cred.Method)

	// 如果凭证是必需的，确保删除后该方法仍有已验证的凭证
	if isRequired {
		// 同一方法可能有多个已验证凭证，因此需要获取每个方法的计数映射

		// 获取指定方法的所有凭证
		allCreds, err := store.Users.GetAllCreds(uid, cred.Method, false)
		if err != nil {
			return nil, err
		}

		// 检查是否可以安全删除：存在另一个已验证值，
		// 或者该值本身尚未验证
		var okTodelete bool
		for _, cr := range allCreds {
			if (cr.Done && cr.Value != cred.Value) || (!cr.Done && cr.Value == cred.Value) {
				okTodelete = true
				break
			}
		}

		if !okTodelete {
			// 拒绝：这是唯一已验证的凭证，必须提供
			return nil, types.ErrPolicy
		}
	}

	// 凭证非必需，或者该方法有多个已验证凭证
	err := vld.Remove(uid, cred.Value)
	if err != nil {
		if err == types.ErrNotFound {
			// 未找到凭证，无法删除
			err = nil
		}
		return nil, err
	}

	// 移除已删除凭证生成的标签
	var tags []string
	if globals.validators[cred.Method].addToTags {
		// 此错误不应返回给用户
		if utags, err := store.Users.UpdateTags(uid, nil, []string{cred.Method + ":" + cred.Value}, nil); err == nil {
			tags = utags
		} else {
			logs.Warn.Println("delete cred: failed to update tags:", err)
			tags = nil
		}
	} else {
		tags = nil
	}

	return tags, nil
}
