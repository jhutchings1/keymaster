package keymaster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"strings"
)

// PolicyName constructs the policy name form the inputs in a regular fashion. Note: namespaces like 'core-platform' will make policy names with embedded hyphens.  This could be a problem if we ever need to split the policy name to reconstruct the inputs.
func (km *KeyMaster) PolicyName(role string, namespace string, env Environment) (name string, err error) {
	if role == "" {
		err = errors.New("empty role names are not supported")
		return name, err
	}

	if namespace == "" {
		err = errors.New("empty role namespaces are not supported")
		return name, err
	}

	if env == 0 {
		err = errors.New("unsupported environment")
		return name, err
	}

	switch env {
	case Prod:
		name = fmt.Sprintf("%s-%s-%s", PROD_NAME, namespace, role)
	case Stage:
		name = fmt.Sprintf("%s-%s-%s", STAGE_NAME, namespace, role)
	default:
		name = fmt.Sprintf("%s-%s-%s", DEV_NAME, namespace, role)
	}

	return name, err
}

// PolicyPath constructs the path to the policy for the role
func (km *KeyMaster) PolicyPath(role string, namespace string, env Environment) (path string, err error) {
	policyName, err := km.PolicyName(role, namespace, env)
	if err != nil {
		err = errors.Wrapf(err, "failed to create policy name")
		return path, err
	}

	path = fmt.Sprintf("sys/policy/%s", policyName)

	return path, err
}

// NewPolicy creates a new Policy object for a given Role and Environment
func (km *KeyMaster) NewPolicy(role *Role, env Environment) (policy VaultPolicy, err error) {
	payload, err := km.MakePolicyPayload(role, env)
	if err != nil {
		err = errors.Wrapf(err, "failed to create payload")
		return policy, err
	}

	pName, err := km.PolicyName(role.Name, role.Namespace, env)
	if err != nil {
		err = errors.Wrapf(err, "failed to create policy name")
		return policy, err
	}

	pPath, err := km.PolicyPath(role.Name, role.Namespace, env)
	if err != nil {
		err = errors.Wrapf(err, "failed to create policy path")
		return policy, err
	}

	policy = VaultPolicy{
		Name:    pName,
		Path:    pPath,
		Payload: payload,
	}

	return policy, err
}

// MakePolicyPayload is the access policy to a specific secret path.  Policy Payloads give access to a single path, wildcards are not supported.
/* example policy:  We will only ever set 'read'.
{
  "path": {  # alphabetical by key
    "service/sign/cert-issuer": {
      "capabilities": [
        "read"
      ]
    }
  }
}
*/
func (km *KeyMaster) MakePolicyPayload(role *Role, env Environment) (policy map[string]interface{}, err error) {
	policy = make(map[string]interface{})
	pathElem := make(map[string]interface{})

	for _, secret := range role.Secrets {
		secretPath, err := km.SecretPath(secret.Name, secret.Namespace, env)
		if err != nil {
			err = errors.Wrapf(err, "failed to create secret path for %s role %s", secret.Name, role.Name)
			return policy, err
		}

		caps := []interface{}{"read"}
		pathPolicy := map[string]interface{}{"capabilities": caps}
		pathElem[secretPath] = pathPolicy
	}

	// add ability to read own policy
	secretPath, err := km.PolicyPath(role.Name, role.Namespace, env)
	if err != nil {
		err = errors.Wrapf(err, "failed to create policy path")
		return policy, err
	}

	caps := []interface{}{"read"}
	pathPolicy := map[string]interface{}{"capabilities": caps}
	pathElem[secretPath] = pathPolicy
	policy["path"] = pathElem

	return policy, err
}

// WritePolicyToVault does just that.  It takes a vault client and the policy and takes care of the asshattery that is the vault api for policies.
func (km *KeyMaster) WritePolicyToVault(policy VaultPolicy) (err error) {
	// policies are not normal writes, and a royal pain the butt.  Thank you Mitch.
	jsonBytes, err := json.Marshal(policy.Payload)
	if err != nil {
		err = errors.Wrapf(err, "failed to marshal payload for %s", policy.Name)
		return err
	}

	payload := string(jsonBytes)

	payload = base64.StdEncoding.EncodeToString(jsonBytes)

	body := map[string]string{
		"policy": payload,
	}

	reqPath := fmt.Sprintf("/v1/%s", policy.Path)

	r := km.VaultClient.NewRequest("PUT", reqPath)
	if err := r.SetJSONBody(body); err != nil {
		err = errors.Wrapf(err, "failed to set json body on request")
		return err
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	resp, err := km.VaultClient.RawRequestWithContext(ctx, r)
	if err != nil {
		err = errors.Wrapf(err, "policy set request failed")
		return err
	}

	defer resp.Body.Close()

	return err
}

// ReadPolicyFromVault fetches a policy from vault and converts it back to a VaultPolicy object
func (km *KeyMaster) ReadPolicyFromVault(path string) (policy VaultPolicy, err error) {
	s, err := km.VaultClient.Logical().Read(path)
	if err != nil {
		return policy, err
	}

	var policyName string

	if s != nil {
		n, ok := s.Data["name"]
		if ok {
			name, ok := n.(string)
			if ok {
				policyName = name
			}
		}

		raw := s.Data["rules"]
		rawRules, ok := raw.(string)
		if ok {
			jsonString := strings.ReplaceAll(rawRules, "\\", "")

			payload := make(map[string]interface{})

			err := json.Unmarshal([]byte(jsonString), &payload)
			if err != nil {
				err = errors.Wrapf(err, "failed to unmarshal policy rules")
				return policy, err
			}

			policy = VaultPolicy{
				Name:    policyName,
				Path:    path,
				Payload: payload,
			}

			return policy, err
		}
	}

	return policy, err
}

// DeletePolicyFromVault  deletes a policy from vault.  It only deletes the policy, it doesn't do anything about the auth method or the secrets.
func (km *KeyMaster) DeletePolicyFromVault(path string) (err error) {
	_, err = km.VaultClient.Logical().Delete(path)
	if err != nil {
		err = errors.Wrapf(err, "failed to delete path %s", path)
		return err
	}

	return err
}

/*
	LDAP Auth can access only dev secrets

	k8s dev auth can access dev secrets
	k8s stage auth can access stage secrets
	k8s prod auth can access prod secrets

	aws dev auth can access dev secrets
	aws stage auth can access stage secrets
	aws prod auth can access prod secrets

	tls prod auth can access prod secrets
	do we need tls stage / dev access at all?

*/
