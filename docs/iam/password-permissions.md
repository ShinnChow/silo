# Password and user-management permissions

SILO separates a user's own password change from creating users or resetting
another user's password. The same `add-user` administration endpoint and mcli
commands continue to work; the authenticated caller and target access key
determine which permission is checked.

| Request | Permission | Evaluation |
| --- | --- | --- |
| Change the caller's own password | `admin:ChangeMyPassword` | Allowed for an internal user with an attached policy unless explicitly denied. |
| Create another user or reset another user's password | `admin:CreateUser` | Requires an explicit Allow; an explicit Deny wins. |

The Console's Change Password button uses `admin:ChangeMyPassword`.
`admin:CreateUser` continues to control user administration. The password change
still requires the current password. STS and service-account credentials cannot
change their parent user's password; root credentials and external identity
provider passwords remain outside this endpoint.

## Existing policies

A saved `Deny admin:CreateUser` still prevents user creation and password resets
for other users. It no longer prevents the caller from changing their own
password. If an existing policy used that deny to lock the caller's password,
add the new action to the same Deny statement before upgrading:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Deny",
      "Action": ["admin:CreateUser", "admin:ChangeMyPassword"]
    }
  ]
}
```

Preserve any existing conditions on that statement. To lock only the caller's
password while allowing separately granted user administration, deny only
`admin:ChangeMyPassword`. An existing `Deny admin:*` denies both actions.
An Allow cannot override a matching Deny.

The built-in `readonly` policy now grants its original S3 read operations
without a CreateUser deny. The added `consolereadonly` policy also grants
ListBucket for Console browsing. Neither grants user administration or S3
writes. Both permit self-service password changes unless another policy denies
them. Combining either read-only policy with an explicit CreateUser Allow is
supported.

Saved policies and user overrides of canned policies are preserved on upgrade.
A saved copy of the old read-only policy therefore retains its CreateUser deny:
it still blocks a separate CreateUser Allow, even though self-service password
changes now use the new action. Review that deny explicitly if combining old
read-only policies with user-administration grants.

## Coordinated upgrade

Upgrade SILO Server, silo-pkg and Console together, including the Console
embedded in Server. Update mcli's shared package and SDK pins as part of the
same maintained stack. Mixed versions disagree about the self-service action
and may show a button the Server refuses, hide an allowed operation, or fail to
enforce a new password-specific deny on an old Server. Complete a rolling
Server upgrade before relying on the new permission split.

This adopts [minio/pkg #262](https://github.com/minio/pkg/pull/262). Upstream
MinIO compatibility remains best effort; the supported integration target is
`pgsty/silo`.
