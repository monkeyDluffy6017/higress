package main

const (
	Success = "ai-gateway.success"

	CodeBuildModelsFailed        = "ai-gateway.build_models_failed"
	CodeSendModelsResponseFailed = "ai-gateway.send_models_response_failed"
	CodeUnauthorized             = "ai-gateway.unauthorized"
	CodeNoToken                  = "ai-gateway.no_token"
	CodeInvalidToken             = "ai-gateway.invalid_token"
	CodeTokenParseFailed         = "ai-gateway.token_parse_failed"
	CodeNoUserID                 = "ai-gateway.no_userid"
	CodeNoEmployeeNumber         = "ai-gateway.no_employee_number"
	CodeModelPermissionDenied    = "ai-gateway.model_permission_denied"
	CodeTotalQuotaError          = "ai-gateway.total_quota_error"
	CodeUsedQuotaError           = "ai-gateway.used_quota_error"
	CodeInsufficientQuota        = "ai-gateway.insufficient_quota"
	CodeDeductionFailed          = "ai-gateway.deduction_failed"
	CodeDeductionInconsistent    = "ai-gateway.deduction_inconsistent"
	CodeInvalidParams            = "ai-gateway.invalid_params"
	CodeGenericError             = "ai-gateway.error"
	CodeRedisError               = "ai-gateway.redis_error"
	CodeInvalidQuotaFormat       = "ai-gateway.invalid_quota_format"
	CodeInvalidQuotaValue        = "ai-gateway.invalid_quota_value"
	CodeStarRequired             = "ai-gateway.star_required"
)
