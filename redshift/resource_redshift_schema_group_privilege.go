package redshift

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

//https://docs.aws.amazon.com/redshift/latest/dg/r_GRANT.html
//https://docs.aws.amazon.com/redshift/latest/dg/r_REVOKE.html

/*
TODO Id is schema_id || '_' || group_id, not sure if that is consistent for terraform --frankfarrell
*/
func redshiftSchemaGroupPrivilege() *schema.Resource {
	return &schema.Resource{
		Create: resourceRedshiftSchemaGroupPrivilegeCreate,
		Read:   resourceRedshiftSchemaGroupPrivilegeRead,
		Update: resourceRedshiftSchemaGroupPrivilegeUpdate,
		Delete: resourceRedshiftSchemaGroupPrivilegeDelete,
		Exists: resourceRedshiftSchemaGroupPrivilegeExists,
		Importer: &schema.ResourceImporter{
			State: resourceRedshiftSchemaGroupPrivilegeImport,
		},

		Schema: map[string]*schema.Schema{
			"schema_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"group_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"select": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"insert": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"update": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"delete": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"references": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"create": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"usage": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
		},
	}
}

func resourceRedshiftSchemaGroupPrivilegeExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	// Exists - This is called to verify a resource still exists. It is called prior to Read,
	// and lowers the burden of Read to be able to assume the resource exists.
	client := meta.(*Client).db

	var privilegeId string

	err := client.QueryRow(`select distinct id from (
		select nsp.oid || '_' || pu.grosysid as id
		from pg_group pu, pg_default_acl acl, pg_namespace nsp
		where acl.defaclnamespace = nsp.oid and
		array_to_string(acl.defaclacl, '|') LIKE '%' || 'group ' || pu.groname || '=%'
		and nsp.oid || '_' || pu.grosysid = $1
		union
		select nsp.oid || '_' || pu.grosysid as id
		from  pg_group pu, pg_namespace nsp
		where array_to_string(nsp.nspacl, '|') LIKE '%' || 'group ' || pu.groname || '=%'
			and nsp.oid || '_' || pu.grosysid = $1
		)`,
		d.Id()).Scan(&privilegeId)

	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

func resourceRedshiftSchemaGroupPrivilegeCreate(d *schema.ResourceData, meta interface{}) error {

	redshiftClient := meta.(*Client).db

	tx, txErr := redshiftClient.Begin()

	if txErr != nil {
		panic(txErr)
	}

	grants := validateGrants(d)
	schemaGrants := validateSchemaGrants(d)

	if len(grants) == 0 && len(schemaGrants) == 0 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error creating schema group privilege: rollback failed: %v", rollbackErr)
		}
		return NewError("Must have at least 1 privilege")
	}

	schemaName, schemaOwner, schemaErr := GetSchemaInfoForSchemaId(tx, d.Get("schema_id").(int))
	if schemaErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting schema info: rollback failed: %v", rollbackErr)
		}
		log.Print(schemaErr)
		return schemaErr
	}

	if isSystemSchema(schemaOwner) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting schema info: rollback failed: %v", rollbackErr)
		}
		return NewError("Privilege creation is not allowed for system schemas, schema=" + schemaName)
	}

	groupName, groupErr := GetGroupNameForGroupId(tx, d.Get("group_id").(int))
	if groupErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting group name: rollback failed: %v", rollbackErr)
		}
		log.Print(groupErr)
		return groupErr
	}

	if len(grants) > 0 {
		var grantPrivilegeStatement = "GRANT " + strings.Join(grants[:], ",") + " ON ALL TABLES IN SCHEMA " + schemaName + " TO GROUP " + groupName

		if _, err := tx.Exec(grantPrivilegeStatement); err != nil {
			log.Print(err)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				log.Printf("error granting privileges; unable to rollback: %v", rollbackErr)
			}
			log.Print(err)
			return err
		}

		var defaultPrivilegesStatement = "ALTER DEFAULT PRIVILEGES IN SCHEMA " + schemaName + " GRANT " + strings.Join(grants[:], ",") + " ON TABLES TO GROUP " + groupName
		if _, err := tx.Exec(defaultPrivilegesStatement); err != nil {
			log.Print(err)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				log.Printf("error altering privileges; unable to rollback: %v", rollbackErr)
			}
			log.Print(err)
			return err
		}
	}

	if len(schemaGrants) > 0 {
		var grantPrivilegeSchemaStatement = "GRANT " + strings.Join(schemaGrants[:], ",") + " ON SCHEMA " + schemaName + " TO GROUP " + groupName
		if _, err := tx.Exec(grantPrivilegeSchemaStatement); err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				log.Printf("error granting privileges; unable to rollback: %v", rollbackErr)
			}
			log.Print(err)
			return err
		}
	}

	d.SetId(fmt.Sprint(d.Get("schema_id").(int)) + "_" + fmt.Sprint(d.Get("group_id").(int)))

	readErr := readRedshiftSchemaGroupPrivilege(d, tx)

	if readErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error granting privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(readErr)
		return readErr
	}

	commitErr := tx.Commit()
	if commitErr != nil {
		log.Print("Error committing transaction: ", commitErr)
		return commitErr
	}

	return nil
}

func resourceRedshiftSchemaGroupPrivilegeRead(d *schema.ResourceData, meta interface{}) error {

	redshiftClient := meta.(*Client).db
	tx, txErr := redshiftClient.Begin()
	if txErr != nil {
		panic(txErr)
	}

	err := readRedshiftSchemaGroupPrivilege(d, tx)

	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error reading Redshift schema groiup privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}

	commitErr := tx.Commit()
	if commitErr != nil {
		log.Print("Error committing transaction: ", commitErr)
		return commitErr
	}

	return nil
}

func readRedshiftSchemaGroupPrivilege(d *schema.ResourceData, tx *sql.Tx) error {
	var (
		usagePrivilege      bool
		createPrivilege     bool
		selectPrivilege     bool
		updatePrivilege     bool
		insertPrivilege     bool
		deletePrivilege     bool
		referencesPrivilege bool
	)

	var hasPrivilegeQuery = `
			select
			cast(bool_or(decode(charindex('r',split_part(split_part(array_to_string(defaclacl, '|'),'group ' || pu.groname,2 ) ,'/',1)),0,0,1)) as int)  as select,
			cast(bool_or(decode(charindex('w',split_part(split_part(array_to_string(defaclacl, '|'),'group ' || pu.groname,2 ) ,'/',1)),0,0,1)) as int)  as update,
			cast(bool_or(decode(charindex('a',split_part(split_part(array_to_string(defaclacl, '|'),'group ' || pu.groname,2 ) ,'/',1)),0,0,1)) as int)  as insert,
			cast(bool_or(decode(charindex('d',split_part(split_part(array_to_string(defaclacl, '|'),'group ' || pu.groname,2 ) ,'/',1)),0,0,1)) as int)  as delete,
			cast(bool_or(decode(charindex('x',split_part(split_part(array_to_string(defaclacl, '|'),'group ' || pu.groname,2 ) ,'/',1)),0,0,1)) as int)  as references
			from pg_group pu, pg_default_acl acl, pg_namespace nsp
			where acl.defaclnamespace = nsp.oid and
			array_to_string(acl.defaclacl, '|') LIKE '%' || 'group ' || pu.groname || '=%'
			and nsp.oid = $1
			and pu.grosysid = $2`

	privilegesError := tx.QueryRow(hasPrivilegeQuery, d.Get("schema_id").(int), d.Get("group_id").(int)).Scan(&selectPrivilege, &updatePrivilege, &insertPrivilege, &deletePrivilege, &referencesPrivilege)

	if privilegesError != nil && privilegesError != sql.ErrNoRows {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(privilegesError)
		return privilegesError
	}

	var hasSchemaPrivilegeQuery = `
			select
			case
				when charindex('U',split_part(split_part(array_to_string(nspacl, '|'), 'group ' || pu.groname,2 ) ,'/',1)) > 0 then 1
				else 0
			end as usage,
			case
				when charindex('C',split_part(split_part(array_to_string(nspacl, '|'),'group ' || pu.groname,2 ) ,'/',1)) > 0 then 1
				else 0
			end as create
			from pg_group pu, pg_namespace nsp
			where array_to_string(nsp.nspacl, '|') LIKE '%' || 'group ' || pu.groname || '=%'
			and nsp.oid = $1
			and pu.grosysid = $2`

	schemaPrivilegesError := tx.QueryRow(hasSchemaPrivilegeQuery, d.Get("schema_id").(int), d.Get("group_id").(int)).Scan(&usagePrivilege, &createPrivilege)

	if schemaPrivilegesError != nil && schemaPrivilegesError != sql.ErrNoRows {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting schema privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(schemaPrivilegesError)
		return schemaPrivilegesError
	}

	d.Set("usage", usagePrivilege)
	d.Set("create", createPrivilege)
	d.Set("select", selectPrivilege)
	d.Set("insert", insertPrivilege)
	d.Set("update", updatePrivilege)
	d.Set("delete", deletePrivilege)
	d.Set("references", referencesPrivilege)

	return nil
}

func resourceRedshiftSchemaGroupPrivilegeUpdate(d *schema.ResourceData, meta interface{}) error {
	redshiftClient := meta.(*Client).db
	tx, txErr := redshiftClient.Begin()

	if txErr != nil {
		panic(txErr)
	}

	grants := validateGrants(d)
	schemaGrants := validateSchemaGrants(d)

	if len(grants) == 0 && len(schemaGrants) == 0 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting grants; unable to rollback: %v", rollbackErr)
		}
		return NewError("Must have at least 1 privilege")
	}

	schemaName, _, schemaErr := GetSchemaInfoForSchemaId(tx, d.Get("schema_id").(int))
	if schemaErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting schema info for schema ID; unable to rollback: %v", rollbackErr)
		}
		log.Print(schemaErr)
		return schemaErr
	}

	groupName, groupErr := GetGroupNameForGroupId(tx, d.Get("group_id").(int))
	if groupErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting group name for group id; unable to rollback: %v", rollbackErr)
		}
		log.Print(groupErr)
		return groupErr
	}

	//Would be much nicer to do this with zip if possible
	if err := updatePrivilege(tx, d, "select", "SELECT", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updatePrivilege(tx, d, "insert", "INSERT", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error adding privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updatePrivilege(tx, d, "update", "UPDATE", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error updating privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updatePrivilege(tx, d, "delete", "DELETE", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error deleting privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updatePrivilege(tx, d, "references", "REFERENCES", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error granting references privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updateSchemaPrivilege(tx, d, "usage", "USAGE", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error granting update schema privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}
	if err := updateSchemaPrivilege(tx, d, "create", "CREATE", schemaName, groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error creating schema privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}

	commitErr := tx.Commit()
	if commitErr != nil {
		log.Print("Error committing transaction: ", commitErr)
		return commitErr
	}

	return nil
}

func resourceRedshiftSchemaGroupPrivilegeDelete(d *schema.ResourceData, meta interface{}) error {

	redshiftClient := meta.(*Client).db
	tx, txErr := redshiftClient.Begin()

	if txErr != nil {
		panic(txErr)
	}

	schemaName, _, schemaErr := GetSchemaInfoForSchemaId(tx, d.Get("schema_id").(int))
	if schemaErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting schema info for schema ID; unable to rollback: %v", rollbackErr)
		}
		log.Print(schemaErr)
		return schemaErr
	}

	groupName, groupErr := GetGroupNameForGroupId(tx, d.Get("group_id").(int))
	if groupErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error getting group name for group ID; unable to rollback: %v", rollbackErr)
		}
		log.Print(groupErr)
		return groupErr
	}

	if _, err := tx.Exec("REVOKE ALL ON  ALL TABLES IN SCHEMA " + schemaName + " FROM GROUP " + groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error revoking privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}

	if _, err := tx.Exec("ALTER DEFAULT PRIVILEGES IN SCHEMA " + schemaName + " REVOKE ALL ON TABLES FROM GROUP " + groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error altering default privileges; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}

	if _, err := tx.Exec("REVOKE ALL ON SCHEMA " + schemaName + " FROM GROUP " + groupName); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			log.Printf("error revoking all privileges on schema; unable to rollback: %v", rollbackErr)
		}
		log.Print(err)
		return err
	}

	commitErr := tx.Commit()
	if commitErr != nil {
		log.Print("Error committing transaction: ", commitErr)
		return commitErr
	}

	return nil
}

func resourceRedshiftSchemaGroupPrivilegeImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	if err := resourceRedshiftSchemaGroupPrivilegeRead(d, meta); err != nil {
		return nil, err
	}
	return []*schema.ResourceData{d}, nil
}

func updatePrivilege(tx *sql.Tx, d *schema.ResourceData, attribute string, privilege string, schemaName string, groupName string) error {
	if !d.HasChange(attribute) {
		return nil
	}

	if d.Get(attribute).(bool) {
		if _, err := tx.Exec("GRANT " + privilege + " ON ALL TABLES IN SCHEMA " + schemaName + " TO  GROUP " + groupName); err != nil {
			return err
		}
		if _, err := tx.Exec("ALTER DEFAULT PRIVILEGES IN SCHEMA " + schemaName + " GRANT " + privilege + " ON TABLES TO GROUP " + groupName); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec("REVOKE " + privilege + " ON ALL TABLES IN SCHEMA " + schemaName + " FROM GROUP " + groupName); err != nil {
			return err
		}
		if _, err := tx.Exec("ALTER DEFAULT PRIVILEGES IN SCHEMA " + schemaName + " REVOKE " + privilege + " ON TABLES FROM GROUP " + groupName); err != nil {
			return err
		}
	}
	return nil
}

func isSystemSchema(schemaOwner int) bool {
	return schemaOwner == 1
}

func updateSchemaPrivilege(tx *sql.Tx, d *schema.ResourceData, attribute string, privilege string, schemaName string, groupName string) error {
	if !d.HasChange(attribute) {
		return nil
	}

	if d.Get(attribute).(bool) {
		if _, err := tx.Exec("GRANT " + privilege + " ON SCHEMA " + schemaName + " TO  GROUP " + groupName); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec("REVOKE " + privilege + " ON SCHEMA " + schemaName + " FROM GROUP " + groupName); err != nil {
			return err
		}
	}
	return nil
}

func validateGrants(d *schema.ResourceData) []string {
	var grants []string

	if v, ok := d.GetOk("select"); ok && v.(bool) {
		grants = append(grants, "SELECT")
	}
	if v, ok := d.GetOk("insert"); ok && v.(bool) {
		grants = append(grants, "INSERT")
	}
	if v, ok := d.GetOk("update"); ok && v.(bool) {
		grants = append(grants, "UPDATE")
	}
	if v, ok := d.GetOk("delete"); ok && v.(bool) {
		grants = append(grants, "DELETE")
	}
	if v, ok := d.GetOk("references"); ok && v.(bool) {
		grants = append(grants, "REFERENCES")
	}

	return grants
}

func validateSchemaGrants(d *schema.ResourceData) []string {
	var grants []string

	if v, ok := d.GetOk("create"); ok && v.(bool) {
		grants = append(grants, "CREATE")
	}
	if v, ok := d.GetOk("usage"); ok && v.(bool) {
		grants = append(grants, "USAGE")
	}

	return grants
}

// errorString is a trivial implementation of error.
type errorString struct {
	s string
}

func (e *errorString) Error() string {
	return e.s
}

// New returns an error that formats as the given text.
func NewError(text string) error {
	return &errorString{text}
}
